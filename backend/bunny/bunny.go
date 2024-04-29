package bunny

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/cache"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const (
	endpointURL   = "https://storage.bunnycdn.com"
	minSleep      = 10 * time.Millisecond
	maxSleep      = 1 * time.Minute
	decayConstant = 1 // bigger for slower decay, exponential
)

func init() {

	fs.Register(&fs.RegInfo{
		Name:        "bunny",
		Description: "BunnyCDN Storage Zone",
		NewFs:       NewFs,
		Options: []fs.Option{
			{
				Name:      "storagezone",
				Help:      "Storage Zone Name",
				Required:  true,
				Sensitive: false,
			},
			{
				Name:      "key",
				Help:      "API Key",
				Required:  true,
				Sensitive: true,
			},
		},
	})

}

type Options struct {
	StorageZone string `config:"storagezone"`
	Key         string `config:"key"`
}

type Fs struct {
	name       string         // name of this remote
	root       string         // the path we are working on if any
	opt        Options        // parsed config options
	ci         *fs.ConfigInfo // global config
	features   *fs.Features   // optional features
	srv        *rest.Client   // connection to bunny cdn
	pacer      *fs.Pacer      // pacer for API calls
	httpClient *http.Client   // http client for download/upload
	cache      *cache.Cache   // cache for directory lists
}

type Object struct {
	fs      *Fs
	remote  string
	size    int64
	modTime time.Time
	name    string
	sha256  string
}

func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if opt.StorageZone == "" {
		return nil, errors.New("storage zone not found")
	}
	if opt.Key == "" {
		return nil, errors.New("access key not found")
	}
	ci := fs.GetConfig(ctx)
	f := &Fs{
		name:       name,
		opt:        *opt,
		root:       root,
		ci:         ci,
		srv:        rest.NewClient(fshttp.NewClient(ctx)),
		pacer:      fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		httpClient: fshttp.NewClient(ctx),
		cache:      cache.New(),
	}
	f.features = (&fs.Features{}).Fill(ctx, f)

	return f, nil

}

// List the objects and directories in dir into entries.  The
// entries can be returned in any order but should be for a
// complete directory.
//
// dir should be "" to list the root, and should not have
// trailing slashes.
//
// This should return ErrDirNotFound if the directory isn't
// found.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {

	list, err := f.list(ctx, dir)
	if err != nil {
		return nil, err
	}

	for _, file := range list.items {
		remote := path.Join(dir, file.ObjectName)
		if file.IsDirectory {
			dir := fs.NewDir(remote, file.ModTime())
			entries = append(entries, dir)
			// log.Print("Dir Found: /", dir.Remote())
		} else {
			entries = append(entries, f.newObjectWithInfo(remote, &file))
		}
	}
	return entries, nil
}

func (f *Fs) Features() *fs.Features {
	return f.features
}

// NewObject finds the Object at remote.  If it can't be found
// it returns the error ErrorObjectNotFound.
//
// If remote points to a directory then it should return
// ErrorIsDir if possible without doing any extra work,
// otherwise ErrorObjectNotFound.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	// if remote == "" {
	// return nil, errors.New("unable to get object for root dir")
	// }
	filename := path.Base(remote)
	list, err := f.list(ctx, remote)

	if err != nil {
		return nil, err
	}
	for _, entry := range list.Files(f) {
		entryName := path.Base(entry.Remote())
		if entryName == filename {
			return entry, nil
		}
	}
	for _, d := range list.Dirs() {
		entryName := path.Base(d.Remote())
		if entryName == filename {
			return nil, fs.ErrorIsDir
		}
	}
	return nil, fs.ErrorObjectNotFound
}

// Setup a new http client request with credentials
func (f *Fs) newRequest(ctx context.Context, method string, remote string, in io.Reader, options []fs.OpenOption) (req *http.Request, err error) {
	url := f.getFullFilePath(remote, true)
	if strings.HasSuffix(remote, "/") {
		url = url + "/"
	}
	req, err = http.NewRequestWithContext(ctx, method, url, in)
	if err == nil {
		if options != nil {
			for k, v := range fs.OpenOptionHeaders(options) {
				req.Header.Add(k, v)
			}
		}
		req.Header.Add("AccessKey", f.opt.Key)
	}
	return req, err
}

// Put in to the remote path with the modTime given of the given size
//
// When called from outside an Fs by rclone, src.Size() will always be >= 0.
// But for unknown-sized objects (indicated by src.Size() == -1), Put should either
// return an error or upload it properly (rather than e.g. calling panic).
//
// May create the object even if it returns an error - if so
// will return the object and the error, otherwise will return
// nil and the error
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (o fs.Object, err error) {
	var resp *http.Response
	var req *http.Request
	req, err = f.newRequest(ctx, "PUT", src.Remote(), in, options)
	if err != nil {
		return nil, err
	}
	srcHash, err := src.Hash(ctx, hash.SHA256)
	if err == nil && srcHash != "" {
		req.Header.Add("Checksum", strings.ToUpper(srcHash))
	}
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.httpClient.Do(req)
		if err == nil && resp.StatusCode != 201 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return false, errors.New("unable to upload file (status: " + fmt.Sprintf("%0.2d", resp.StatusCode) + ")")
		}
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	f.clearDirCache(filepath.Dir(src.Remote()))

	if resp == nil {
		return nil, errors.New("no response returned (put)")
	}
	if resp.StatusCode == 201 {
		return &Object{
			fs:      f,
			remote:  src.Remote(),
			name:    src.Remote(),
			size:    -1,
			modTime: time.Now(),
		}, nil
	}
	return nil, errors.New("http put failed")
}

// Mkdir makes the directory (container, bucket)
//
// Shouldn't return an error if it already exists
//
// The bunny.net storage zone will auto-create directories based on
// the file upload path
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	if !strings.HasSuffix(dir, "/") {
		dir = dir + "/"
	}

	var resp *http.Response
	var req *http.Request
	body := bytes.NewBufferString("{}")
	req, err := f.newRequest(ctx, "PUT", dir, body, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")

	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.httpClient.Do(req)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 201 {
		return errors.New("unable to create directory")
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// Rmdir removes the directory (container, bucket) if empty
//
// Return an error if it doesn't exist or isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) (err error) {
	var resp *http.Response
	var req *http.Request

	req, err = f.newRequest(ctx, "DELETE", dir+"/", nil, nil)
	if err != nil {
		return err
	}

	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.httpClient.Do(req)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("no response returned (delete)")
	}
	if resp.StatusCode == 404 {
		return fs.ErrorDirNotFound
	}
	if resp.StatusCode != 200 {
		return errors.New("unable to delete dir, status code:" + fmt.Sprintf("%d", resp.StatusCode))
	}
	return nil

}

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// Hashes returns the supported hash sets. (bunny.net only support SHA256)
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.SHA256)
}

// Precision of the remote
func (f *Fs) Precision() time.Duration {
	return fs.ModTimeNotSupported
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return fmt.Sprintf("BunnyCDN Storage Pool: %s path %s", f.opt.StorageZone, f.root)
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if resp != nil && resp.StatusCode == 429 {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return true, pacer.RetryAfterError(err, time.Duration(5*time.Second))
	}
	return false, err
}

func (f *Fs) newObjectWithInfo(remote string, file *DirItem) fs.Object {
	o := &Object{
		fs:      f,
		remote:  remote,
		size:    file.Length,
		modTime: file.ModTime(),
		name:    file.ObjectName,
		sha256:  strings.ToLower(file.Checksum),
	}
	return o
}

func (o *Object) Fs() fs.Info {
	return o.fs
}

func (o *Object) Size() int64 {
	return o.size
}

func (o *Object) ModTime(context.Context) time.Time {
	return o.modTime
}

func (o *Object) Remote() string {
	return o.remote
}

func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) {
	if ty == hash.SHA256 {
		return o.sha256, nil
	}
	return "", hash.ErrUnsupported
}

func (o *Object) Storable() bool {
	return true
}

func (o *Object) SetModTime(ctx context.Context, t time.Time) error {

	return fs.ErrorCantSetModTime
}

func (f *Fs) getFullFilePath(remote string, incRoot bool) string {
	baseUrl := "/" + f.opt.StorageZone
	if incRoot {
		baseUrl = endpointURL + baseUrl
	}
	subPath := path.Join(f.root, remote)
	return baseUrl + "/" + rest.URLPathEscape(strings.TrimLeft(subPath, "/"))
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	var resp *http.Response
	var req *http.Request

	reqUrl := o.fs.getFullFilePath(o.remote, true)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	for k, v := range fs.OpenOptionHeaders(options) {
		req.Header.Add(k, v)
	}
	req.Header.Add("AccessKey", o.fs.opt.Key)
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.httpClient.Do(req)
		if err == nil && resp.StatusCode != 200 {
			return false, errors.New("File not found (Status: " + fmt.Sprintf("%d", resp.StatusCode) + ")")
		}
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 200 {
		return resp.Body, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil, errors.New("file not found")

}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (err error) {

	var resp *http.Response
	var req *http.Request

	req, err = o.fs.newRequest(
		ctx,
		http.MethodPut,
		o.remote,
		in,
		options,
	)
	if err != nil {
		return err
	}

	srcHash, err := src.Hash(ctx, hash.SHA256)
	if err == nil && srcHash != "" {
		req.Header.Add("Checksum", strings.ToUpper(srcHash))
	}

	// msg, err := httputil.DumpRequest(req, false)
	// if err == nil {
	// fmt.Print(string(msg))
	// }

	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.httpClient.Do(req)
		if err == nil && resp.StatusCode != 201 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return true, errors.New("File not uploaded (Status: " + fmt.Sprintf("%d", resp.StatusCode) + ")")
		}
		return shouldRetry(ctx, resp, err)
	})
	o.fs.clearDirCache(filepath.Dir(src.Remote()))
	return err

}

func (o *Object) Remove(ctx context.Context) (err error) {
	var resp *http.Response
	var req *http.Request

	req, err = http.NewRequestWithContext(ctx, "DELETE", o.fs.getFullFilePath(o.remote, true), nil)
	req.Header.Add("AccessKey", o.fs.opt.Key)

	if err != nil {
		return err
	}
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.httpClient.Do(req)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	o.fs.clearDirCache(filepath.Dir(o.Remote()))
	if resp.StatusCode != 200 {
		return errors.New("Failed to delete file: " + o.remote)
	}
	return nil
}

var _ fs.Object = (*Object)(nil)

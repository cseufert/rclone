package bunny

import (
	"context"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/rest"
)

type DirList struct {
	dir   string
	items []DirItem
}

type DirItem struct {
	Guid            string // The unique identifier for this file
	StorageZoneName string // Name of the storage zone
	Path            string // Path to this file
	ObjectName      string // Filename
	Length          int64  // Size of the file
	LastChanged     string // Timestamp file was uploaded
	ServerId        int
	ArrayNumber     int
	IsDirectory     bool   // Entry is for a directory
	UserId          string // UUID of user who created file
	ContentType     string // File MIME Type
	DateCreated     string // Date file was first uploaded
	StorageZoneId   int    // Numeric ID of the storage zone
	Checksum        string // Checksum of file contents
	ReplicatedZones string // Zone names
}

func (i *DirItem) ModTime() time.Time {
	// 2017-03-10T03:06:48.203

	t, err := time.Parse("2006-01-02T15:04:05.999", i.LastChanged)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (i *DirItem) FullPath(d *DirList) string {
	if d.dir == "" {
		return i.ObjectName
	} else {
		return d.dir + "/" + i.ObjectName
	}
}

func (f *Fs) clearDirCache(dir string) {
	if dir == "." {
		dir = ""
	}
	f.cache.Delete(dir)
}

// Retrieve a directory listing form bunny and store in cache
func (f *Fs) list(ctx context.Context, dir string) (list *DirList, err error) {
	value, found := f.cache.GetMaybe(dir)

	if found {
		list = value.(*DirList)
	} else {
		reqPath := f.getFullFilePath(dir, false)
		// log.Print("List Path: ", reqPath+"/")
		var response []DirItem
		opts := rest.Opts{
			RootURL:      endpointURL,
			Method:       "GET",
			Path:         reqPath + "/",
			ExtraHeaders: map[string]string{"Accept": "application/json", "AccessKey": f.opt.Key},
		}
		err = f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &response)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return nil, err
		}
		list = &DirList{
			dir:   dir,
			items: response,
		}

		// f.cache.Put(dir, list)
	}
	return list, nil
}

func (d *DirList) Dirs() fs.DirEntries {
	var list []fs.DirEntry
	for _, i := range d.items {
		if i.IsDirectory {
			list = append(list, fs.NewDir(i.FullPath(d), i.ModTime()))
		}
	}

	return list
}

func (d *DirList) Files(fs *Fs) (list []fs.Object) {
	// list := []Object{}
	for _, i := range d.items {
		if i.IsDirectory {
			list = append(list, &Object{
				fs:      fs,
				size:    i.Length,
				modTime: i.ModTime(),
				name:    i.ObjectName,
				remote:  i.FullPath(d),
				sha256:  strings.ToLower(i.Checksum),
			})
		}
	}
	return list
}

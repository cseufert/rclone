---
title: "Bunny Storage"
description: "Read and write files to Bunny.net Storage Zones"
versionIntroduced: "master"
---

# {{< icon "fa fa-cloud" >}} Bunny Storage

The Bunny STorage remote provides access to bunny.net storage zones, for read and
write. This can be used to clone or back-up storage zones. 

Paths are specified within a storage zone. Example the path `/test` will resolve to 
`https://storage.bunnycdn.com/my-zone-name/test`.

## Configuration

Here is an example of how to make a remote called `remote`. First run:

     rclone config

This will guide you through an interactive setup process:

```
No remotes found, make a new one?
n) New remote
s) Set configuration password
q) Quit config
n/s/q> n
name> remote
Type of storage to configure.
Choose a number from below, or type in your own value
...
xx / BunnyCDN Storage Zone
   \ (bunny)
...
Storage> bunny

Option storagezone.
Storage Zone Name
Enter a value.
storagezone> my-zone-name

Option key.
API Key
Enter a value.
key> xxxxxxxxxxxx (from bunny.net console)

Edit advanced config?
y) Yes
n) No (default)
y/n> n


Configuration complete.
Options:
- type: bunny
- storagezone: my-zone-name
- key: xxxxxxxxxxxx
Keep this "TestRemote" remote?
y) Yes this is OK (default)
e) Edit this remote
d) Delete this remote
y/e/d> y

Current remotes:

Name                 Type
====                 ====
remote               bunny

e) Edit existing remote
n) New remote
d) Delete remote
r) Rename remote
c) Copy remote
s) Set configuration password
q) Quit config
e/n/d/r/c/s/q> q
```

This new remote is called `remote` and can now be used like this

See all the top level directories 

    rclone lsd remote:

List the contents of a directory

    rclone ls remote:directory

sync the remote `directory` to `/home/local/directory`, delete any excess files.

    rclone sync --interactive remote:directory /home/local/directory

clone the remote `directory` to `/home/local/directory`

    rclone sync /home/local/directory remote:directory

### checksums

sha256 checksums are calculated by bunny storage zones, these can be used to verify files

### standard options

#### --bunny-storagezone

this is the name of the storagezone, which can be set when creating the storage zones
or retrieved from the bunny.net management console.

properties:

- config:      storagezone
- env var:     rclone_bunny_storagezone
- type:        string
- required:    true

#### --bunny-key

api key for this storage zone, each storage zone has its own key. this can be access via
the **FTP & API access** section of the bunny.net management console.

Properties:

- Config:      key
- Env Var:     RCLONE_BUNNY_KEY
- Type:        string
- Required:    true


### Limitations

Currently the bunny storage backend only accesses the storage via the main Falkenstein data
center. (That limitation could be removed in the future, volunteers welcome)

`rclone about` is not supported by the bunny backend. This means that rclone mount or the use
policy `mfs` (most free space) as a member of an rclone union remote.
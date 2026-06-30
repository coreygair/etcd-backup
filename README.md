# etcd-backup

A really simple tool for snapshotting and defragmenting an etcd cluster.

Supports saving database snapshots to a local filesystem or any S3-compatible object store,
encrypting snapshots with [age](https://github.com/filosottile/age)
and defragmenting the database after snapshotting.

## Install

`go install github.com/coreygair/etcd-backup`

Also available as a Docker container: [`coreygair/etcd-backup`](https://hub.docker.com/r/coreygair/etcd-backup)

## Usage

```
etcd-backup --endpoints <etcd-client-endpoint>,... [options]
```

### etcd connection/authentication

- `--endpoints` — comma-separated list of etcd endpoints to connect to. Repeatable, **Required**.
- `--cert` — path to a client TLS certificate file for the etcd connection.
- `--key` — path to the corresponding client TLS key file.
- `--cacert` — path to a CA bundle used to verify the etcd server's TLS certificate.
- `--insecure-skip-tls-verify` — skip verification of the etcd server certificate. Use with care.
- `--username` — username for etcd authentication.
- `--password` — password for etcd authentication.

The `--cert`, `--key` and `--cacert` options enable TLS, copying the interface of [etcdctl](https://github.com/etcd-io/etcd/tree/main/etcdctl) which uses the same flags. If none of these are specified, the client connection is made without TLS.

### Snapshot

`etcd-backup --endpoints ... --snapshot-store file:///var/snapshots`

`--snapshot-store` selects where the snapshot is written. Two schemes are supported:

- `file:///path/to/dir` — writes the snapshot as a file in the given local directory. The
  directory is created if it does not exist.
- `s3://bucket[?region=...&endpoint=...&usePathStyle=...]` — uploads the snapshot as an object
  in an S3 (or S3-compatible) bucket. The host is the bucket name. Supported query parameters:
  - `region` — the bucket region.
  - `endpoint` — a custom endpoint URL, for non-AWS S3-compatible stores (e.g. MinIO, Garage).
    Setting this also enables path-style addressing unless `usePathStyle` overrides it.
  - `usePathStyle` — `true` or `false`, to force path-style or virtual-hosted-style addressing.

  S3 credentials are read from `S3_ACCESS_KEY_ID` and `S3_SECRET_ACCESS_KEY` (which must be set
  together) if present; otherwise the standard AWS SDK credential chain is used.

If `--snapshot-store` is not specified, no snapshot is taken.

Snapshots are named using a unix timestamp taken at the time they are written to storage - `{unix-time-in-seconds}.db`.

### Encryption

`etcd-backup --endpoints ... --snapshot-store ... --recipient age1...`

When at least one recipient is supplied the snapshot is encrypted with [age](https://github.com/FiloSottile/age)
before being written; otherwise it is stored unencrypted.

- `--recipient` — an age recipient (public key) to encrypt the snapshot for. Repeatable.
- `--recipient-file` — path to a file of newline-separated age recipients. Repeatable.

Using multiple recipient flags is additive - all recipients specified will be able to decrypt the snapshot.

Encrypted snapshots use the `.db.age` extension to differentiate them from unencrypted snapshots.

### Defragmentation

`etcd-backup --endpoints ... --defrag --defrag-utilization 0.5 --defrag-size 128Mi`

etcd automatically compacts the database while running but does not reclaim storage space until a defragmentation operation is issued.
`--defrag` runs a defragmentation on each node sequentially after snapshotting.

- `--defrag[=<mode>]` — defragment etcd nodes after snapshotting. Passing the flag bare is
  equivalent to `--defrag=cluster`. Modes:
  - `cluster` — discover all cluster members via the member list and defragment each one.
  - `endpoints` — only defragment the nodes passed to `--endpoints`.
- `--defrag-utilization` — a value between `0.0` and `1.0`; only defragment nodes whose
  utilization (`DbInUse / DbSize`) is less than or equal to this. Default `1.0` (no filtering).
- `--defrag-size` — only defragment nodes whose `DbSize` exceeds this. Accepts a plain byte
  count or a Kubernetes-style [resource quantity](https://kubernetes.io/docs/reference/kubernetes-api/definitions/quantity-resource/) such as `512Mi` or `1Gi`.

Defragmentation runs against each target sequentially. Nodes with an empty database are always
skipped, and a non-zero exit code is returned after all targets were attempted if any target fails.

### Miscellaneous

- `--debug` — enable client-side debug logging.
- `--timeout` — overall timeout for the run, as a Go duration (e.g. `30s`, `2m`). Default `30s`.

## License

GNU GPLv3

# Exact-image cold capacity qualification

A new admitted digest cannot be pulled on the production host until its actual
cold lifecycle fits the retained capacity policy. A smaller compressed image is
not a substitute for byte/inode measurements. These tools reuse the October 5
site qualification's Docker 29.1.3/containerd 2.2.1 private-daemon procedure.

First prepare the exact candidate using the normal live admission command and
retain `deploy.py status` as a private baseline. Both current and prior must be
distinct, recorded images with no unfinished deployment or maintenance.

The runtime directory contains the original authenticated archives and their
extracted `runtime/docker/*` and `runtime/containerd/bin/*` executables. Obtain
`docker-29.1.3.tgz` from Docker's official Linux static x86_64 download and
`containerd-2.2.1-linux-amd64.tar.gz` from containerd's official v2.2.1 release.
The reviewed SHA-256 values in `capacity_contract.py` authenticate both archives;
the tool also compares each executable with its archive member before starting
anything. Existing cached bytes can be reused.

```sh
sudo unshare --mount --propagation private -- \
  python3 deploy/kamal-site/capacity_measure.py \
  --candidate /private/candidate.json --baseline /private/site-before.json \
  --runtime /private/cached-runtime --output /private/fresh-capacity
```

The output directory must be new. The disposable 8 GiB ext4 loop filesystem and
its Docker/containerd data and sockets are separate from production. The outer
filesystem needs 11 GiB free initially and retains a sampled 3 GiB floor. The
tool disables Docker bridge/iptables changes, binds the site only to loopback
port 19042, and never connects to the production SSH host. It pulls candidate,
active and prior into an empty store, samples free bytes/inodes every 100 ms,
boots and probes exact build identities and public routes, switches and rolls
back, then removes only its private prior image. Failed runs retain a rejected
report and terminate their private daemons and unmount the private filesystem.

After a successful measurement, register it using the normal pinned SSH and
supervisor ownership path:

```sh
SITE_SSH_KEY=/private/operator-key python3 deploy/kamal-site/capacity_register.py \
  --candidate /private/candidate.json --baseline /private/site-before.json \
  --measurement /private/fresh-capacity/measurement.json \
  --output /private/capacity-registration.json
```

Registration repeats live release admission, recomputes the peaks from raw
samples, and checks the exact candidate and retained input hashes. Under the
existing supervisor, it rechecks the actual Docker/containerd versions and
overlayfs driver, the active/prior state and the capacity policy
CAS. It only raises measured envelopes and reserves, requires actual backing
filesystem headroom, retains an exclusive private backup, and atomically fsyncs
the new policy. It never pulls, switches or prunes production images. State or
policy drift requires a new baseline and measurement. A lost SSH result uses
the existing explicit supervisor reconciliation, not an automatic retry.

This is capacity evidence only. Actual deployment, rollback acceptance and the
fresh 24-hour adoption interval are separate retained gates.

#!/bin/sh
# entrypoint.sh — bring up the rig's fake production host: a real sshd whose
# `docker` is the fixture-replaying shim.
#
# The daemon under test dials this over the network with the shipping SSH
# client, so everything sshd normally enforces (host key exchange, publickey
# auth, exec-channel command handling) is genuinely exercised. Only the
# container runtime behind `docker` is simulated.
set -eu

FIXTURE_DIR="${FIXTURE_DIR:-/fixtures}"
KEYS=/etc/loghost/authorized_keys
mkdir -p "$FIXTURE_DIR" /var/run/sshd /etc/loghost

# A per-container host key. Regenerated on every `compose up`, which is what
# makes the daemon's TOFU pin a real first-sight decision rather than a
# constant baked into the image.
if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
    ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
fi

# The authorized key is installed at scenario time from the collecting node's
# own agent identity (lib_logwatch.sh), so no key material is baked into the
# image or committed to the repo. sshd requires this file to be owned by root
# and not group- or world-writable.
touch "$KEYS"
chown root:root "$KEYS"
chmod 644 "$KEYS"
chown -R logwatch:logwatch "$FIXTURE_DIR"

# A default, STABLE port inventory. The collector raises a notify-class line
# whenever the exposure fingerprint changes, so leaving this empty (or letting
# it wobble between pulls) would inject unrelated alerts into every monitoring
# assertion.
if [ ! -f "$FIXTURE_DIR/ports.inventory" ]; then
    printf 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2|order-sync|0.0.0.0:8080->8080/tcp\n' \
        > "$FIXTURE_DIR/ports.inventory"
    chown logwatch:logwatch "$FIXTURE_DIR/ports.inventory"
fi

exec /usr/sbin/sshd -D -e

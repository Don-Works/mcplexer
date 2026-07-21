#!/usr/bin/env sh
# Container entrypoint: prep the data dir, then exec the daemon in the
# foreground so docker tracks PID 1.
set -eu

mkdir -p /data/p2p

# Every container is a genuinely independent collaboration identity. The
# private Ed25519 key stays in its own named volume and is exposed to the
# daemon only through a private ssh-agent socket; scenarios read only .pub.
if [ ! -f /data/identity_ed25519 ]; then
    ssh-keygen -q -t ed25519 -N '' \
        -C "mcplexer-integration@$(hostname)" \
        -f /data/identity_ed25519
fi
chmod 600 /data/identity_ed25519
rm -f /data/ssh-agent.sock
eval "$(ssh-agent -s -a /data/ssh-agent.sock)" >/dev/null
ssh-add /data/identity_ed25519 >/dev/null
export SSH_AUTH_SOCK=/data/ssh-agent.sock

# The first request to the daemon also auto-creates the api-key. We give it
# a head-start so curl-from-host can read /data/api-key right after boot
# without racing the first request.
if [ ! -f /data/api-key ]; then
    # `mcplexer serve` does the same thing; this is just an explicit nudge so
    # the file exists when the healthcheck flips the container to "healthy".
    :
fi

# `MCPLEXER_MODE=http` + `MCPLEXER_HTTP_ADDR=0.0.0.0:3333` are baked into
# the image env; we don't take any flags. exec hands PID 1 to the daemon
# so a docker stop / compose down sends SIGTERM straight through.
exec /usr/local/bin/mcplexer serve

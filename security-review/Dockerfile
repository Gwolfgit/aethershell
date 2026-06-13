# Disposable adversarial test harness for aethershell.
# Two unprivileged users (victim/attacker) to probe the cross-user boundary,
# plus python3 + procps + util-linux for raw socket abuse and PTY-driven tests.
FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 procps util-linux socat netcat-openbsd strace less vim-tiny \
    && rm -rf /var/lib/apt/lists/*

# Disposable users. victim runs aetherd; attacker tries to break in.
RUN useradd -m -u 2000 -s /bin/bash victim \
    && useradd -m -u 2001 -s /bin/bash attacker

# Build the binaries from the (disposable) source copy.
WORKDIR /build
COPY src/ /build/
RUN go build -buildvcs=false -o /usr/local/bin/aetherd  ./cmd/aetherd \
    && go build -buildvcs=false -o /usr/local/bin/aether   ./cmd/aether \
    && go build -buildvcs=false -o /usr/local/bin/aether-connect ./cmd/aether-connect \
    && chmod 0755 /usr/local/bin/aether*

# Per-user runtime dirs mimicking pam_systemd's /run/user/<uid> (0700, owned).
RUN install -d -m 0700 -o victim   -g victim   /run/user/2000 \
    && install -d -m 0700 -o attacker -g attacker /run/user/2001

# Harness scripts + probe.
COPY harness/ /harness/
RUN chmod +x /harness/*.sh

WORKDIR /harness
CMD ["sleep", "infinity"]

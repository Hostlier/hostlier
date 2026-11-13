FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server openssh-client sudo procps \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd \
    && touch /run/hostlier-ssh-test-container \
    && printf 'Port 2222\nListenAddress 127.0.0.1\nPasswordAuthentication no\n' > /etc/ssh/sshd_config.d/00-hostlier-test.conf
COPY sshaccess.test /sshaccess.test
ENV HOSTLIER_SSH_CONTAINER_TEST=1
CMD ["sh", "-c", "/usr/sbin/sshd && exec /sshaccess.test -test.v -test.run TestManagedSSHLive"]

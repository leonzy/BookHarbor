# syntax=docker/dockerfile:1.6
# ----------------------------------------
# 第一阶段，拉取 node 基础镜像并安装依赖，执行构建
FROM node:20-alpine AS builder
ARG BUILD_COUNTRY=""
ARG TARGETARCH

WORKDIR /build
RUN if [ "x${BUILD_COUNTRY}" = "xCN" ]; then \
    echo "using repo mirrors for ${BUILD_COUNTRY}"; \
    npm config set registry https://registry.npmmirror.com; \
    fi

COPY app/package.json app/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci

# spa build mode will clear ssr build data, run it first
COPY app/ /build/
ARG TALEBOOK_BASE_PATH=""
ENV TALEBOOK_BASE_PATH=${TALEBOOK_BASE_PATH}
RUN mkdir -p /app-ssr/ /app-static/
RUN npm run build
RUN ls -al
RUN cp -r .output package* /app-ssr/
RUN npm run build-spa
RUN rm -rf dist && cp -r .output/public dist
RUN if [ ! -f dist/index.html ]; then cp dist/200.html dist/index.html; fi
RUN cp -r dist package* /app-static/


# ----------------------------------------
# 第一阶段之二，编译 Go 存储层（booksvr）
# 纯静态二进制，不依赖 libc，可直接拷进运行镜像。
FROM golang:1.22-alpine AS go-builder
ARG BUILD_COUNTRY=""
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /build/booksvr
RUN if [ "x${BUILD_COUNTRY}" = "xCN" ]; then \
    echo "using repo mirrors for ${BUILD_COUNTRY}"; \
    go env -w GOPROXY=https://goproxy.cn,direct; \
    fi

# 先只拷依赖清单，让依赖层可缓存
COPY booksvr/go.mod booksvr/go.sum ./
RUN go mod download

COPY booksvr/ /build/booksvr/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/booksvr ./cmd/booksvr && \
    /out/booksvr --version


# ----------------------------------------
# 第二阶段，应用运行基线
# 基础镜像由 https://github.com/talebook/talebook-base 独立维护和发布
FROM talebook/talebook-base:slim-v8.5.0 AS application-base
ARG BUILD_COUNTRY=""
ARG TARGETARCH
ARG TARGETVARIANT

USER root
RUN mkdir -p /var/lib/apt/lists/partial && \
    chmod -R 0755 /var/lib/apt/lists/ && \
    if [ "x${BUILD_COUNTRY}" = "xCN" ]; then \
        echo "using repo mirrors for ${BUILD_COUNTRY}"; \
        \
        if [ -f /etc/apt/sources.list ]; then \
            sed -i 's@deb.debian.org/debian@mirrors.aliyun.com/debian@g' /etc/apt/sources.list; \
            sed -i 's@security.debian.org@mirrors.aliyun.com/debian-security@g' /etc/apt/sources.list; \
        fi; \
        \
        if [ -d /etc/apt/sources.list.d ]; then \
            find /etc/apt/sources.list.d -name "*.list" -exec sed -i 's@deb.debian.org/debian@mirrors.aliyun.com/debian@g' {} \; ; \
            find /etc/apt/sources.list.d -name "*.list" -exec sed -i 's@security.debian.org@mirrors.aliyun.com/debian-security@g' {} \; ; \
        fi; \
        \
        echo "deb http://mirrors.aliyun.com/debian trixie main contrib non-free" > /etc/apt/sources.list; \
        echo "deb http://mirrors.aliyun.com/debian trixie-updates main contrib non-free" >> /etc/apt/sources.list; \
        echo "deb http://mirrors.aliyun.com/debian-security trixie-security main contrib non-free" >> /etc/apt/sources.list; \
        \
        pip config set global.index-url https://mirrors.aliyun.com/pypi/simple/; \
    fi

RUN apt-get update && \
    apt-get install -y --no-install-recommends ffmpeg unar && \
    rm -rf /var/lib/apt/lists/*

# 针对 ARM32 架构的特殊处理
RUN if [ "$TARGETARCH" = "arm" ] && [ "$TARGETVARIANT" = "v7" ]; then \
    echo "Building for ARM32 (ARMv7) architecture"; \
    # 确保使用兼容的包架构
    dpkg --add-architecture armhf || true; \
    fi

# Install the Talebook-owned main config, then let an unprivileged Nginx bind 80/443.
COPY conf/nginx/nginx.conf /etc/nginx/nginx.conf
RUN if ! id -u talebook > /dev/null 2>&1; then \
    useradd -u 911 -U -d /var/www/talebook -s /bin/false talebook && \
    usermod -G users talebook && \
    groupmod -g 911 talebook; \
fi && \
    setcap cap_net_bind_service=+ep /usr/sbin/nginx && \
    getcap /usr/sbin/nginx | grep -Fq 'cap_net_bind_service=ep'


# Native Go backend; Python remains only for Calibre tools and image-owned management.
FROM application-base AS server
COPY requirements.txt /tmp/
RUN --mount=type=cache,target=/root/.cache/pip python3 -m pip install -r /tmp/requirements.txt
COPY scripts/install_tomato_downloader.py /tmp/talebook-runtime-installer/scripts/
COPY document/licenses/tomato-novel-downloader-MIT.txt document/licenses/tomato-novel-downloader-NOTICE.md /tmp/talebook-runtime-installer/document/licenses/
ARG TARGETARCH
RUN case "$TARGETARCH" in amd64|arm64) python3 /tmp/talebook-runtime-installer/scripts/install_tomato_downloader.py --directory /opt/tomato --arch "$TARGETARCH" ;; esac && rm -rf /tmp/talebook-runtime-installer

FROM go-builder AS test
RUN adduser -D -u 911 talebooktest
ENV HOME=/tmp GOCACHE=/tmp/talebook-go-cache
USER 911:911
CMD ["go", "test", "./..."]

FROM server AS production
ARG TALEBOOK_BASE_PATH=""
ARG GIT_VERSION=""
ARG GIT_COMMIT=""
ARG UPGRADE_SEQUENCE="0"
ARG TARGETARCH
ARG TARGETVARIANT
ENV TALEBOOK_BASE_PATH=${TALEBOOK_BASE_PATH} TZ=Asia/Shanghai LANG=C.UTF-8 PUID=1000 PGID=1000 TALEBOOK_UPGRADE_MODE=spa
WORKDIR /var/www/talebook
RUN printf "%s\n" "$GIT_VERSION" > candle-reader-version.txt
RUN mkdir -p /data/log/nginx /data/books/library /data/books/settings /data/books/ssl /data/books/logo /data/books/upload /data/books/convert /var/www/talebook/status /prebuilt
COPY docker/ docker/
COPY resources/ resources/
COPY conf/nginx/ssl.* /data/books/ssl/
COPY conf/nginx/talebook.conf /etc/nginx/conf.d/talebook.conf
COPY conf/supervisor/talebook.conf /etc/supervisor/conf.d/talebook.conf
RUN python3 -m docker.configure_base_path /etc/nginx/conf.d/talebook.conf
COPY docker/status_page.html status/status_page.html
COPY --from=builder /app-static/ app/
COPY --from=go-builder /out/booksvr /usr/local/bin/booksvr
RUN cp /usr/local/bin/booksvr /var/www/talebook/booksvr && chmod 0755 /usr/local/bin/booksvr booksvr docker/start.sh && \
    calibredb list --library-path=/data/books/library --limit=0 && \
    /usr/local/bin/booksvr bootstrap && echo "settings = {}" > /data/books/settings/auto.py && \
    cp -r app/dist/logo/. /data/books/logo/ && rm -rf app/dist/logo && ln -s /data/books/logo app/dist/logo && \
    mv /data/* /prebuilt/ && rm -f /etc/nginx/sites-enabled/default
COPY scripts/upgrade/protocol.py scripts/upgrade/executor.py scripts/upgrade/fingerprint.py /opt/talebook-upgrade/
COPY docker/self_check.py /opt/talebook-upgrade/self_check.py
COPY docker/booksvr-entrypoint.py /opt/talebook-upgrade/booksvr-entrypoint.py
COPY Dockerfile requirements.txt /opt/talebook-contract/
COPY conf/ /opt/talebook-contract/conf/
COPY docker/ /opt/talebook-contract/docker/
COPY booksvr/internal/httpapi/bootstrap.go /opt/talebook-contract/schema/bootstrap.go
COPY booksvr/internal/httpapi/bootstrap.go /var/www/talebook/schema/bootstrap.go
COPY booksvr/internal/httpapi/testdata/user_schema.sql /opt/talebook-contract/schema/user.sql
COPY booksvr/internal/httpapi/testdata/user_schema.sql /var/www/talebook/schema/user.sql
RUN python3 /opt/talebook-upgrade/fingerprint.py /opt/talebook-contract /opt/talebook-upgrade/image.json "$TARGETARCH$TARGETVARIANT" "$GIT_VERSION" "$UPGRADE_SEQUENCE" "$GIT_COMMIT"
EXPOSE 80 443
VOLUME ["/data"]
CMD ["/var/www/talebook/docker/start.sh"]

FROM production AS production-ssr
ENV TALEBOOK_UPGRADE_MODE=ssr
RUN apt-get update && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/*
COPY conf/nginx/server-side-render.conf /etc/nginx/conf.d/talebook.conf
COPY conf/supervisor/server-side-render.conf /etc/supervisor/conf.d/talebook.conf
COPY --from=builder /app-ssr/ /var/www/talebook/app/
RUN python3 -m docker.configure_base_path /etc/nginx/conf.d/talebook.conf

FROM production AS production-spa

FROM production AS dev
ENV TALEBOOK_UPGRADE_MODE=dev
COPY conf/supervisor/dev.conf /etc/supervisor/conf.d/talebook.conf

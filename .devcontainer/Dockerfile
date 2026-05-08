FROM mcr.microsoft.com/devcontainers/base:trixie

RUN apt-get update \
    && export DEBIAN_FRONTEND=noninteractive \
    && apt-get install -y --no-install-recommends \
        build-essential \
        curl \
        git \
        jq \
        postgresql-client \
        redis-tools \
        sqlite3 \
        awscli \
        bubblewrap \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

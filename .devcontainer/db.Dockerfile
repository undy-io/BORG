FROM postgres:18-trixie

# Install pgvector and other common extensions
# Note: package names usually follow 'postgresql-18-<extension-name>'
RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-18-pgvector \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

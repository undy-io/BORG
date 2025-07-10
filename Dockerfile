FROM python:3.12-slim

RUN apt-get update && apt-get install -y curl

# Install Poetry
RUN curl -sSL https://install.python-poetry.org | python3 -
ENV PATH="/root/.local/bin:$PATH"

WORKDIR /app

COPY pyproject.toml poetry.lock* ./

# install dependencies
RUN poetry config virtualenvs.create false \
    && poetry install --no-interaction --no-ansi --no-root --only main

RUN apt-get remove -y curl && apt-get autoremove -y && apt-get clean

# Copy application code
COPY src/ ./src/
COPY config.example.yaml ./config.yaml 
ENV PYTHONPATH=/app/src

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Expose port
EXPOSE 8000

# Run the application
CMD ["/entrypoint.sh"]

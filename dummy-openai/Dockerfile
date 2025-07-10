# syntax=docker/dockerfile:1.5
FROM python:3.11-slim

# install lightweight web server
RUN pip install --no-cache-dir fastapi uvicorn[standard]

WORKDIR /app
COPY main.py /app/main.py

EXPOSE 8000
CMD ["uvicorn", "main:app", "--host=0.0.0.0", "--port=8000"]

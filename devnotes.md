# Development notes

## Building and pushing Docker image

```shell
# Do once per host
docker login gitea.drjosh.dev
docker buildx create --name container --driver=docker-container

# Do for each "dev" release
docker buildx build \
  --tag gitea.drjosh.dev/josh/jrouter:dev \
  --platform linux/arm/v8,linux/amd64 \
  --builder container \
  --push .

# Do for each release
docker buildx build \
  --build-arg VERSION_SUFFIX='' \
  --tag gitea.drjosh.dev/josh/jrouter:latest \
  --tag gitea.drjosh.dev/josh/jrouter:0.0.12 \
  --tag gitea.drjosh.dev/josh/jrouter:0.0 \
  --tag gitea.drjosh.dev/josh/jrouter:0 \
  --platform linux/arm/v8,linux/amd64 \
  --builder container \
  --push .
```

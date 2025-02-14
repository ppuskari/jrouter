# Development notes

## Building and pushing Docker image

```shell
# Do once per host
docker login gitea.drjosh.dev
docker buildx create --name container --driver=docker-container

# Do for each build/push
docker buildx build \
  --tag gitea.drjosh.dev/josh/jrouter:latest \
  --platform linux/arm/v8,linux/amd64 \
  --builder container \
  --push .
```

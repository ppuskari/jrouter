# Development notes

## Building and pushing Docker image

```shell
# Do once per host
docker login gitea.drjosh.dev
docker buildx create --name container --driver=docker-container

# Do for each "dev" release
go tool mage all 0.0.13-dev

# Do for each release
go tool mage all 0.0.13
```

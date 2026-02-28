To build images with the `serverless-gpu-` prefix:
 ./scripts/build-and-push.sh docker.io/gangyi89 $(git rev-parse --short HEAD)


 run this command to allow local to build amd64
 docker buildx create --use --name multiarch-builder || docker buildx use multiarch-builder
docker buildx inspect --bootstrap
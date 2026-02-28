To build the project specify the follow. It will align with current git version and comes with prefix serverless-gpu
 ./build-and-push.sh docker.io/gangyi89 $(git rev-parse --short HEAD) 


 run this command to allow local to build amd64
 docker buildx create --use --name multiarch-builder || docker buildx use multiarch-builder
docker buildx inspect --bootstrap
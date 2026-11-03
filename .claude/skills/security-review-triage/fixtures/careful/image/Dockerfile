# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
ARG BASE_IMAGE=registry.example.com/library/node:24-alpine@sha256:4b3d9475d65571d28cbb19544d3820ec2945e4c8b2f18279394282b8dc3a592e
FROM ${BASE_IMAGE} AS build
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=npm_token NPM_TOKEN_FILE=/run/secrets/npm_token npm ci
ADD --checksum=sha256:4b3d9475d65571d28cbb19544d3820ec2945e4c8b2f18279394282b8dc3a592e https://example.invalid/tool.tar.gz /opt/tool.tar.gz

FROM registry.example.com/library/node:24-alpine@sha256:4b3d9475d65571d28cbb19544d3820ec2945e4c8b2f18279394282b8dc3a592e
COPY --from=build /src /app
USER 10001:10001
CMD ["node", "/app/server.js"]

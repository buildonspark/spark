FROM node:20-slim AS sdk_builder

# Stage 1: build and pack the local @buildonspark/spark-sdk
WORKDIR /app/spark-sdk
COPY sdks/js/packages/spark-sdk/ ./

# Install dependencies, build, and pack the SDK into /tmp/spark-sdk.tgz
RUN npm install && npm run build && npm pack --pack-destination /tmp && \
  mv /tmp/buildonspark-spark-sdk-*.tgz /tmp/spark-sdk.tgz

FROM node:20-slim

# This final stage installs the packed SDK tarball.
WORKDIR /app

# Copy the packed SDK and wallet runner into the runtime image.
COPY --from=sdk_builder /tmp/spark-sdk.tgz /tmp/spark-sdk.tgz
COPY docker-test/wallet-runner.js ./wallet-runner.js

# Create a minimal project and install the locally packed SDK.
RUN npm init -y && npm install /tmp/spark-sdk.tgz

ENV NODE_ENV=production

# Run the wallet runner using the locally built SDK.
CMD ["node", "wallet-runner.js"]

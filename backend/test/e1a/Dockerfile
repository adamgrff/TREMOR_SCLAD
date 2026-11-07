# Test-only image. Supply reviewed immutable image references at T1.
ARG GO_IMAGE
ARG PG_IMAGE
FROM ${GO_IMAGE} AS tests
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/migrations ./internal/migrations
COPY cmd/migrate-extension ./cmd/migrate-extension
COPY migrations ./migrations
RUN CGO_ENABLED=0 go test -c -o /out/migrations.test ./internal/migrations && \
    CGO_ENABLED=0 go build -o /out/migrate-extension ./cmd/migrate-extension

FROM ${PG_IMAGE}
COPY --from=tests /out/ /opt/e1a/
COPY --from=tests /src/migrations /opt/e1a/migrations
COPY test/e1a/run.sh /opt/e1a/run.sh
COPY test/e1a/run-suite.sh /opt/e1a/run-suite.sh
COPY test/e1a/init.sql /docker-entrypoint-initdb.d/10-e1a.sql
RUN sed -i 's/\r$//' /opt/e1a/run.sh /opt/e1a/run-suite.sh && chmod 755 /opt/e1a/run.sh /opt/e1a/run-suite.sh
WORKDIR /opt/e1a/internal/migrations

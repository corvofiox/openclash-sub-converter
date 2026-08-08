# 多阶段构建：编译阶段 + 运行阶段（alpine 精简镜像）
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /osc ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /osc /usr/local/bin/osc
WORKDIR /app
EXPOSE 25500
ENTRYPOINT ["osc"]

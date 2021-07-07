FROM golang:1.16 as builder

WORKDIR /app
ADD . /app
RUN go build -o manager .
RUN strip manager

# ---

FROM openjdk:8-jre-slim
RUN apt-get update
RUN apt-get install curl -y

WORKDIR /app

RUN curl -L https://github.com/Anuken/Mindustry/releases/download/v126.2/server-release.jar > server-release.jar
COPY --from=builder /app/manager /usr/bin/manager
CMD /usr/bin/manager

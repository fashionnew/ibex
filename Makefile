.PHONY: start build

APP_BIN = ibex
APP_VER = 1.0.0

all: build

build:
	@go build -ldflags "-w -s -X main.VERSION=$(APP_VER)" -o $(APP_BIN) ./src/cmd

start_server:
	./$(APP_BIN) server -c ./etc/server.conf

start_agentd:
	./$(APP_BIN) agentd -c ./etc/agentd.conf

pack: build
	tar zcvf $(APP_BIN)-$(APP_VER).tar.gz etc sql $(APP_BIN)

# windows
# go build -ldflags "-w -s -X main.VERSION=$1.0.0" -o ibex.exe ./src/cmd
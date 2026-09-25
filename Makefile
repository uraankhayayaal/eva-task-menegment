# Makefile for eva-similar
#
# Targets:
#   make            build всех бинарей
#   make build      то же самое
#   make run        Одна команда: indexer + linker (в режиме комментариев)
#   make run-all    indexer + linker (comment) + listener
#   make run-indexer / run-linker / run-listener
#   make test / clean / help
#
# Конфиг (токены Eva, Qdrant, пороги) берётся из корневого .env.
# Бинари собираются в ./bin (gitignored).

GO      := go
BIN_DIR := bin
BINS    := indexer listener linker

.PHONY: all build test clean help run run-all run-indexer run-linker run-listener

all: build

build: $(addprefix build-,$(BINS))

build-%:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$* ./cmd/$*

# Одна команда: собирает и запускает indexer и linker (comment) параллельно.
run: build-indexer build-linker
	@echo "==> indexer + linker (LINKER_LINK_MODE=comment)"
	$(MAKE) -j2 run-indexer run-linker

run-all: build
	@echo "==> indexer + linker (comment) + listener"
	$(MAKE) -j3 run-indexer run-linker run-listener

run-indexer: build-indexer
	$(BIN_DIR)/indexer

run-linker: build-linker
	LINKER_LINK_MODE=comment $(BIN_DIR)/linker

run-listener: build-listener
	$(BIN_DIR)/listener

test:
	$(GO) test ./...

clean:
	rm -rf $(BIN_DIR)

help:
	@echo "Цели:"
	@echo "  make build      собрать все бинари в ./bin"
	@echo "  make run        одна команда: indexer + linker (comment mode)"
	@echo "  make run-all    indexer + linker (comment) + listener (webhook)"
	@echo "  make run-indexer | run-linker | run-listener"
	@echo "  make test       запустить go test ./..."
	@echo "  make clean      удалить ./bin"
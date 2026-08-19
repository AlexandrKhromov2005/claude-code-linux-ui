#!/usr/bin/env sh
# Поднимает веб-интерфейс одной командой: собирает фронтенд, встраивает ассеты в
# бинарь (-tags embed_ui — без него отдаётся заглушка), останавливает прошлый
# экземпляр, занявший порт, и запускает сервер.
#
#   ./run.sh                 # 127.0.0.1:8765
#   ./run.sh 127.0.0.1:9000  # другой порт (только loopback)
set -eu

cd "$(dirname "$0")"

ADDR="${1:-127.0.0.1:8765}"
PORT="${ADDR##*:}"

echo "==> Фронтенд (web/)"
[ -d web/node_modules ] || npm --prefix web install
npm --prefix web run build

echo "==> Бинарь с встроенными ассетами"
go build -tags embed_ui -o claude-code-linux-ui ./cmd/claude-code-linux-ui

# Освободить порт от прошлого экземпляра этого же сервера. Паттерн ловит и
# запуск без подкоманды serve, и исторический с ней.
OLD="$(pgrep -f '/claude-code-linux-ui( |$)' 2>/dev/null || true)"
if [ -n "$OLD" ]; then
	echo "==> Останавливаю прошлый экземпляр (PID $OLD)"
	# shellcheck disable=SC2086
	kill $OLD 2>/dev/null || true
	for _ in $(seq 1 50); do
		ss -ltn 2>/dev/null | grep -q ":$PORT " || break
		sleep 0.1
	done
fi

echo "==> Запуск на $ADDR"
exec ./claude-code-linux-ui "$ADDR"

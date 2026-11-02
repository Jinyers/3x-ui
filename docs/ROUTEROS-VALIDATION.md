# Проверки RouterOS low-write

Базовый upstream: `eae4725d99e7274f1b78b78fc1b56179e03ba2ce`.
Локальная среда: Linux amd64, Go 1.27.3, Node 26.13.0,
Xray 26.10.31 (`a4a3dff`), SQLite, отдельная временная БД.

## Выполнено

- `npm run typecheck` и production-сборка frontend — успешно.
- Тесты навигации, sidebar и command palette — 19 тестов в 3 файлах успешно.
- Полный `go test -count=1` для `internal/config`, `internal/xray`,
  `internal/web/service`, `internal/web/service/outbound`, `internal/web/job`,
  `internal/web/controller`, `internal/web` — все 7 пакетов успешно.
- `go build -ldflags '-w -s'` — бинарник панели собран.
- `actionlint 1.7.12` — workflow ARM64/GHCR прошёл проверку.
- `TestDisabledMetricsPersistence` — live-история доступна, файл и `.tmp` не
  создаются, существующие данные/mtime не меняются; без флага запись работает.
- `TestDisabledTrafficStatsNoSQLiteChanges` — повторные ненулевые delta и
  сопутствующие пути учёта дают ноль изменений `total_changes()` на одной
  соединении; транзакции конфигурации и обычный учёт продолжают работать.
- `TestDisabledTrafficStatsSkipsScheduledReset` и
  `TestDisabledTrafficStatsKeepsTuicJournal` — расписание не сбрасывает счётчики,
  старый TUIC journal сохраняется; обычный режим работает.
- `TestLowWriteFlagsPreserveTunRoutingConfig` — полное побайтовое равенство JSON
  конфигурации Xray с TUN inbound, routing, API и policy при обоих режимах.
- `scripts/verify-low-write.py` — настоящая панель и Xray, локальный HTTP origin
  и трафик через Xray HTTP inbound: 70 секунд запросов, неизменная SQLite,
  отсутствие `system_metrics.gob*` после минутного таймера и SIGTERM.
  Контрольный запуск без `XUI_DISABLE_TRAFFIC_STATS` записывает ненулевые
  inbound/outbound-счётчики. Используется тестовая БД без пользовательских данных.

В интеграционном скрипте проверяются логический dump всех таблиц и SHA-256
основного SQLite-файла/WAL/journal до/после периода трафика. После остановки
сравниваются логические данные: штатный checkpoint WAL может менять файлы.
SHM не используется для оценки долговечных записей — это память блокировок SQLite.

## Границы локальной проверки

Полная Docker-сборка была запущена, но локальный Docker с драйвером `vfs` на
диске 8 GiB исчерпал место при подготовке слоёв. Временный cache удалён;
успешная локальная Docker/ARM64-сборка этим отчётом **не заявляется**.
Workflow выполняет нативные ARM64-сборку, интеграционный тест с реальным Xray
и smoke test контейнера **до** публикации в GHCR. Результат конкретного запуска
следует проверять в GitHub Actions, а digest — в его summary.

Тест TUN сравнивает сгенерированную конфигурацию и не открывает TUN устройства
на машине тестов. Физический MikroTik, его `/dev/net/tun`, права Container,
существующие RouterOS маршруты, DNS и policy routing здесь недоступны.
Аппаратную проверку по инструкции `ROUTEROS.md` нужно выполнить перед заменой
рабочего контейнера. Успешный Linux HTTP-тест не заменяет эту проверку.

Первоначальная публикация возвращала HTTP 403: приложение было авторизовано,
но не установлено для аккаунта владельца репозитория. После установки GitHub
App с доступом к `Jinyers/3x-ui` push ветки `feat/routeros-low-write` прошёл.

Ручная сборка этой ветки публикует SHA-тег; общий `routeros-arm64` обновляется
только после сборки default branch. Не устанавливайте образ до успешного
завершения workflow и проверки его digest.

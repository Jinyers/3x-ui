# 3x-ui для MikroTik RouterOS ARM64

Изменения основаны на `Jinyers/3x-ui`, commit
`eae4725d99e7274f1b78b78fc1b56179e03ba2ce`.
Это опциональный режим уменьшения фоновых записей, а не полностью read-only панель.

## Настройки

Установить **в окружении процесса контейнера**, затем перезапустить его:

```text
XUI_DISABLE_METRICS_PERSISTENCE=true
XUI_DISABLE_TRAFFIC_STATS=true
XUI_ENABLE_FAIL2BAN=false
```

Только строка `true` включает каждый флаг; отсутствующее значение и `false`
сохраняют прежнее поведение. Обе оптимизации независимы. В образе они по
умолчанию выключены. `.env` не включается в образ; для RouterOS используйте envlist.

- `XUI_DISABLE_METRICS_PERSISTENCE`: не регистрируется минутное сохранение;
  сама `PersistSystemMetrics` также защищена от любых вызовов, включая shutdown.
  `system_metrics.gob` и временный `.tmp` не создаются и не перезаписываются.
  Существующий снимок не удаляется, его чтение при запуске сохраняется.
  CPU, RAM, дисковая и сетевая нагрузка обновляются каждые 2 секунды;
  история текущего процесса остаётся в RAM и пропадает после перезапуска.
- `XUI_DISABLE_TRAFFIC_STATS`: останавливается опрос Xray для учёта трафика;
  `AddTraffic` inbound/client/outbound, обновление `last_online`, глобальные
  счётчики узлов, принятие удалённых снимков и квитанции TUIC не пишутся в БД.
  Автоматические сбросы счётчиков и replay/создание журнала TUIC выключены.
  Старый журнал TUIC остаётся на диске для обработки после возврата обычного режима.
  Запуск и согласование MTProto, TUIC, AmneziaWG, контроль процесса Xray,
  подписки и обновление маршрутизации продолжают работать.

**Ограничения режима без статистики:** счётчики, скорости клиентов, Xray online
и `last_online` не актуализируются; подписки работают, но их информация о расходе
трафика устаревает. Автоматическое отключение/продление клиентов по квотам и
времени, выполняемое внутри `AddTraffic`, также не работает. Уже отключённые
клиенты/inbound автоматически не включаются. Используйте режим для личного
роутера без биллинга; заранее проверьте квоты, expiry и enabled-состояния.
Ручные изменения, сбросы и сохранение настроек продолжают писать в SQLite.

При управлении удалёнными панелями конфигурация и явные команды сброса
по-прежнему доставляются. Автоматическое принятие конфигурации из traffic
snapshot (adoption), статистика и IP-синхронизация этого пути приостановлены.
Для полноценной работы дерева узлов оставьте статистику включённой. Это не
изменяет локальные inbound/outbound, TUN или правила маршрутизации.

## Какие записи исследованы

| Источник                                                         | Период/условие                         | Результат                                                                  |
| ---------------------------------------------------------------- | -------------------------------------- | -------------------------------------------------------------------------- |
| `ServerController.startTask` → `PersistSystemMetrics`            | 1 минута                               | Отключается первым флагом                                                  |
| `Server.stop` → `PersistSystemMetrics`                           | SIGTERM/SIGHUP/остановка панели        | Защищён тем же флагом                                                      |
| `XrayTrafficJob`                                                 | 5 секунд                               | Отключается вторым флагом                                                  |
| `InboundService.AddTraffic`                                      | опрос, sidecar, генерация Xray-конфига | No-op при втором флаге, включая lifecycle maintenance                      |
| `OutboundService.AddTraffic`                                     | Xray traffic poll                      | No-op при втором флаге                                                     |
| `BumpClientsLastOnline`                                          | соединения Xray/TUIC                   | No-op при втором флаге                                                     |
| MTProto/AmneziaWG/TUIC                                           | 10 секунд и финальный flush            | Согласование работает; общий учёт защищён                                  |
| `NodeTrafficSyncJob`                                             | 5 секунд                               | Конфигурация/ручные сбросы работают, traffic pull/global push пропускаются |
| `AcceptGlobalTraffic`, `SetRemoteTraffic`, `AddTuicTrafficBatch` | API/опрос/replay                       | Защищены до обращения к БД                                                 |
| `PeriodicTrafficResetJob`                                        | час/день/неделя/месяц                  | Автоматические сбросы выключены                                            |
| `CheckClientIpJob`, IP-ban state, очистка IP-истории             | 10 секунд/сутки, если настроены        | Сохранены: обеспечивают IP-лимиты и безопасность                           |
| `NodeHeartbeatJob`                                               | 5 секунд при наличии узлов             | Сохранён: доступность, состояние и уведомления узлов                       |
| Outbound subscription, LDAP, WARP, настройка remote routing      | настройки/расписание                   | Сохранены: могут менять конфигурацию                                       |
| Вход в панель, API tokens, HWID подписок, ручные операции        | запросы пользователя                   | Сохранены                                                                  |
| Миграции и seed SQLite                                           | запуск/обновление                      | Сохранены                                                                  |

Общую очередь `TrafficWriter` отключать нельзя: через неё также проходят
транзакции конфигурации. SQLite journal mode, синхронизация и надёжность настроек
не менялись. Проверять нужно `.db`, `-wal`, `-shm`, а не только mtime основного файла.
Логи панели/Xray, автозагрузка geodata, резервные копии, ACME и включённые
интеграции тоже могут писать на диск. Управляйте их настройками отдельно;
отключение статистики не обещает полного отсутствия записей.

## Удаление рекламы

Удалены SponsorSlot на login/dashboard, страница Sponsors, её пункт меню и
командной палитры, рекламные компоненты, запрос `/sponsors`, серверные маршруты
списка/логотипов и эксклюзивный рекламный HTTP-клиент к репозиторию sponsors.
Справка, обновления, обычные ссылки проекта и служебные индикаторы сохранены.
MTProto adTag относится к настройке пользовательского прокси, поэтому не менялся.

## Сборка и GHCR

Workflow `.github/workflows/docker.yml` работает на нативном ARM64 runner,
собирает frontend, проверяет типы и Go-тесты, собирает Docker target `routeros`.
Публикация только в GHCR текущего форка; Docker Hub secrets не нужны.
Токен `GITHUB_TOKEN` имеет `packages: write`. Pull request выполняет проверки
и сборку без публикации. Ручной запуск: **Actions → RouterOS ARM64 image → Run workflow**.
Выберите ветку с этим workflow. Push в `main` и теги `v*.*.*` также запускают сборку.

Для этого форка:

```text
ghcr.io/jinyers/3x-ui:routeros-arm64
ghcr.io/jinyers/3x-ui:routeros-arm64-<short-sha>
ghcr.io/jinyers/3x-ui:<version-tag>-routeros-arm64
```

Тег `routeros-arm64` обновляется только на default branch. Для установки и отката
предпочтителен тег SHA или digest из summary Actions. В настройках GHCR package
дайте read-доступ нужным пользователям либо сделайте образ публичным. Первый
запуск workflow в форке может потребовать включить Actions и разрешить запись
packages согласно политике аккаунта.

Отдельный Docker target `routeros` исключает fail2ban/Python. Обычная финальная
стадия Dockerfile остаётся прежней. В runtime копируются только собранная панель,
поставляемые upstream Xray/MTProto/geodata, переводы и скрипты. `.dockerignore`
исключает SQLite с WAL/SHM, метрики, env-файлы, ключи, сертификаты, журналы TUIC
и обычные каталоги секретов. Не размещайте секреты под именами исходников.
Никакие пользовательские БД или данные для входа в образ не добавляются.

Локальная сборка (на ARM64 либо с установленной QEMU emulation):

```sh
docker buildx build --platform linux/arm64 --target routeros \
  --load -t 3x-ui:routeros-arm64 .
```

Состав Go/frontend-зависимостей и версия Xray не менялись. `DockerInit.sh`
по-прежнему загружает geodata и последний MTProto sidecar из upstream: это не
полностью воспроизводимая по байтам сборка. Сохраняйте digest успешного образа.

## Экспорт и установка в RouterOS

На машине с Docker (подставьте SHA-тег из завершённой сборки):

```sh
IMAGE=ghcr.io/jinyers/3x-ui:routeros-arm64-<short-sha>
docker pull --platform linux/arm64 "$IMAGE"
docker image inspect "$IMAGE" --format '{{.Os}}/{{.Architecture}}'
docker save --output 3x-ui-arm64.tar "$IMAGE"
```

Ожидаемая архитектура: `linux/arm64`. Передайте `3x-ui-arm64.tar` на внешний
диск RouterOS через Files/SFTP. Используется **docker save**, сохраняющий
конфигурацию образа, ENTRYPOINT и ENV. `docker export` для установки не нужен.

Нужны RouterOS ARM64 с пакетом Container и включённым container mode, место
на внешнем диске, рабочие DNS и сеть контейнера. Включение device-mode на
некоторых устройствах требует физического подтверждения по инструкции MikroTik.

Пример установки **в уже настроенную контейнерную сеть**. Замените `usb1`,
`veth-xui`, путь tar и каталоги на реальные; каталог rootfs должен быть новым:

```routeros
/container/envs/add list=xui-env key=XUI_DISABLE_METRICS_PERSISTENCE value=true
/container/envs/add list=xui-env key=XUI_DISABLE_TRAFFIC_STATS value=true
/container/envs/add list=xui-env key=XUI_ENABLE_FAIL2BAN value=false
/container/envs/add list=xui-env key=TZ value=UTC
/container/mounts/add name=xui-db src=usb1/xui-db dst=/etc/x-ui
/container/add file=usb1/3x-ui-arm64.tar interface=veth-xui root-dir=usb1/xui-root-v1 envlist=xui-env mounts=xui-db logging=yes start-on-boot=yes comment=xui-low-write
/container/start [find where comment=xui-low-write]
```

Каталог `usb1/xui-db` содержит постоянные настройки и БД; rootfs — заменяемый.
Если уже используются сертификаты, отдельные конфиги или дополнительные mounts,
добавьте их в список `mounts`. База данных должна принадлежать только одному
запущенному экземпляру панели. Начальный пароль генерируется при первом запуске
и показывается один раз в журнале; смените его и ограничьте доступ к панели
файрволом. Существующая БД сохраняет порт, webBasePath и пользователей.

Панель по умолчанию слушает 2053; Docker EXPOSE не создаёт RouterOS NAT/firewall.
Используйте IP контейнера и сохранённый webBasePath. Существующие veth, NAT,
policy routing, DNS и правила исключения трафика самого прокси не заменяйте.

**TUN:** патч не меняет генерацию/применение Xray TUN, API, policy/stats Xray,
маршруты и права процесса. Сохраняйте уже рабочую схему доступа к TUN вашего
RouterOS. Наличие `/dev/net/tun` и разрешения на изменение маршрутов зависят от
версии RouterOS/ядра и настроек Container. Образ сам не предоставляет эти права.
Проверка на обычном Linux не подтверждает их наличие на MikroTik. Если используется
userspace TUN WireGuard (`noKernelTun`), это отдельный механизм от системного TUN.

## Обновление и откат

1. Сохраните digest старого образа, параметры контейнера, mounts/envlist и сеть.
2. Остановите контейнер штатно, дождитесь остановки Xray и скопируйте весь каталог
   `xui-db`, включая сопутствующие файлы. Не копируйте только живой `.db` без WAL.
3. Загрузите новый ARM64 tar. Создайте новый контейнер с новым `root-dir`, той же
   сетью, envlist и mounts, пока старый остановлен. Убедитесь, что параметров
   достаточно для прежнего TUN и дополнительных конфигураций.
4. Запустите новый экземпляр. Проверьте панель, подписку, Xray, TUN и контрольный
   запрос клиента через фактический маршрут, DNS, внешний IP и отсутствие петли.
5. При откате остановите новый экземпляр. Если обновление мигрировало БД,
   восстановите её остановленную копию перед запуском старого образа.

Не используйте встроенный self-update панели для обновления этого контейнера:
он может заменить пропатченный бинарник upstream-версией. Обновляйте образ целиком.

## Проверки и перенос на новый upstream

```sh
cd frontend
npm ci
npm run typecheck
npm run build
cd ..
go test ./internal/config ./internal/web/service ./internal/web/job \
  ./internal/web/service/outbound ./internal/web/controller ./internal/web \
  ./internal/xray -count=1
go build -o build/x-ui .
```

`TestDisabledMetricsPersistence` проверяет отсутствие файла и `.tmp`, неизменность
содержимого/mtime существующего файла и работу истории в RAM. Тест SQLite
использует `total_changes()` на одном соединении: ненулевые delta inbound/client/
outbound, `last_online`, global/node/TUIC пути дают ноль изменений с флагом;
запись настроек и обычный учёт без флага продолжают работать. Отдельные тесты
проверяют автоматические сбросы и сохранность старого журнала TUIC.

Автоматическая проверка с настоящим Xray (assets — каталог `bin` из образа):

```sh
python3 scripts/verify-low-write.py --panel build/x-ui --assets /path/to/bin
```

Она использует только временную БД, локальный HTTP origin и HTTP inbound Xray,
сравнивает таблицы и SHA-256 SQLite/WAL/journal в течение 70 секунд, проверяет
SIGTERM и возвращение стандартного учёта. В CI запускается до публикации.

Для аппаратной приёмки прогоните не менее 70 секунд реального клиентского трафика
с включёнными флагами и затем штатно остановите контейнер. Убедитесь, что метрик
и `.tmp` нет либо старые hash/mtime не изменились; счётчики SQLite не меняются.
Для атрибуции фоновых записей на Linux используйте `strace -ff -yy` с
`openat,write,pwrite64,fsync,fdatasync,rename,unlink`, учитывая WAL/SHM. Повторите
без флага статистики и убедитесь, что ненулевой трафик обновляет счётчики.
Проверяйте сетевые операции на тестовой БД, а не на пользовательской.

При переносе держите отдельные commits для флагов, рекламы и упаковки/документации;
создайте ветку от нового upstream, выполните cherry-pick и проверьте diff.
Обязательно повторно найдите фактические точки записи:

```sh
rg -n 'system_metrics|PersistSystemMetrics|\.gob|AddTraffic|last_online' internal
rg -n 'TrafficWriter|TrafficReset|TrafficBatch|Journal|GlobalTraffic|SetRemoteTraffic' internal
rg -n 'AddJob|AddFunc|Flush.*Traffic' internal/web
rg -n 'SponsorSlot|useSponsorsQuery|sponsorsURL|/sponsors' frontend/src internal/web
```

Проверьте новые sidecar, shutdown/restart-пути, API-приёмники статистики и
изменения cron. Не переносите патч вслепую при изменении роли `AddTraffic`.
Не отключайте общий DB/TrafficWriter, heartbeat, reconcile или подписки для
устранения статистических записей. Запустите проверки снова и отдельно проведите
аппаратную проверку RouterOS TUN/маршрутизации. Фактические результаты этой
реализации приведены в `ROUTEROS-VALIDATION.md`.

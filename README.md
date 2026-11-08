# TripGo — лабораторная работа №3

Сервис поездок для курса «Разработка микросервисов на Go». Он создаёт, отдаёт и
завершает поездки, сохраняет маршрут и запрашивает координаты водителя для
устаревших активных поездок. Данные и история статусов хранятся в PostgreSQL;
доступны `/health`, `/ready` и остановка с ожиданием активных операций.

## Требования

- Go 1.24 или новее;
- `tripgoctl` из репозитория инфраструктуры курса;
- PostgreSQL, observability-коллектор и Push Service, поднятые через `tripgoctl`;
- [golangci-lint v2.15.0](https://github.com/golangci/golangci-lint/releases/tag/v2.15.0) для воспроизводимого `make lint`.

`oapi-codegen` и `goose` закреплены в `go.mod`: команды вызывают их через
`go tool`, поэтому отдельная установка не нужна.

## Быстрый запуск

```bash
tripgoctl cluster start
tripgoctl environment start
tripgoctl connect

# environment start создаёт .env с актуальными адресами сервисов.
# Таймауты и прочие значения по умолчанию берём из .env.example,
# затем настройки окружения курса перекрывают их.
set -a
. ./.env.example
. ./.env
set +a

make migrate
make run
```

Проверка:

```bash
curl -fsS http://localhost:8080/health
curl -fsS http://localhost:8080/ready

curl -i -X POST http://localhost:8080/api/v1/trips \
  -H 'content-type: application/json' \
  -d '{
    "user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c",
    "driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5",
    "start_point":{"latitude":59.9398,"longitude":30.3146},
    "end_point":{"latitude":59.929,"longitude":30.3626},
    "price":1450
  }'
```

Скопируйте `id` из ответа POST в `TRIP_ID`. Пока поездка активна, добавьте
точку маршрута, затем прочитайте маршрут и завершите поездку:

```bash
TRIP_ID=1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111 # замените на id из ответа
curl -fsS http://localhost:8080/api/v1/trips/$TRIP_ID
curl -i -X POST http://localhost:8080/api/v1/trips/$TRIP_ID/positions \
  -H 'content-type: application/json' \
  -d '{"coordinates":{"latitude":59.9343,"longitude":30.3351},"recorded_at":"2026-09-01T10:05:00Z"}'
curl -fsS http://localhost:8080/api/v1/trips/$TRIP_ID/positions
curl -fsS -X POST http://localhost:8080/api/v1/trips/$TRIP_ID/finish
```

Для записи точки укажите актуальное время RFC 3339, не дальше минуты в будущем;
дата в примере лишь показывает формат.

## Команды

| Команда | Назначение |
|---|---|
| `make generate` | обновить HTTP-типы из OpenAPI и GoMock-моки HTTP-действий и юзкейсов |
| `make migrate` | применить все миграции |
| `make migrate-down` | откатить все миграции |
| `make run` | запустить сервис |
| `make build` | собрать все пакеты |
| `make test` | выполнить unit-тесты с race detector и покрытием |
| `make test-integration` | применить миграции к указанной тестовой БД и дважды выполнить интеграционные тесты |
| `make lint` | запустить `golangci-lint` |

Для миграций нужен `DATABASE_URL`. Контракт OpenAPI остаётся в
`contracts/openapi/trip-service.openapi.yaml`; `oapi-codegen.yaml` генерирует
семь операций: пять из ЛР1 и POST/GET точек маршрута.

## Как сдавать

[ЛР1](https://github.com/UnusualNick/template/pull/1) принята в `main`.
[ЛР2](https://github.com/UnusualNick/template/pull/2) отправлена из `homework/02`
в `main` того же форка. ЛР3 выполняется в `homework/03` от обновлённой базы
PR2; PR3 пока не опубликован. Остальные правила сдачи есть в `grading.md`
репозитория курса.

## Конфигурация

Сервис проверяет переменные окружения до запуска HTTP-сервера.

| Переменная | Пример | Назначение |
|---|---|---|
| `HTTP_ADDR` | `:8080` | адрес HTTP-сервера |
| `HTTP_READ_TIMEOUT` | `10s` | общий таймаут чтения запроса |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | таймаут чтения заголовков |
| `HTTP_WRITE_TIMEOUT` | `15s` | таймаут записи ответа |
| `HTTP_IDLE_TIMEOUT` | `60s` | таймаут keep-alive соединения |
| `LOG_LEVEL` | `info` | уровень структурированных логов |
| `SHUTDOWN_TIMEOUT` | `10s` | общий бюджет graceful shutdown |
| `DATABASE_URL` | `postgres://...` | DSN PostgreSQL |
| `DATABASE_MAX_CONNS` | `10` | верхняя граница пула |
| `DATABASE_MIN_CONNS` | `2` | нижняя граница пула |
| `DATABASE_MAX_CONN_LIFETIME` | `30m` | максимальная жизнь соединения |
| `DATABASE_CONNECT_TIMEOUT` | `5s` | таймаут подключения и стартового ping |
| `DATABASE_QUERY_TIMEOUT` | `3s` | таймаут каждого SQL-запроса и readiness |
| `POSITION_CHECK_INTERVAL` | `30s` | интервал проверки устаревших поездок |
| `POSITION_STALE_AFTER` | `60s` | допустимый возраст последней точки или начала поездки |
| `POSITION_WORKERS` | `4` | число обработчиков запросов к Push Service |
| `POSITION_QUEUE_SIZE` | `100` | ёмкость очереди ожидания |
| `PUSH_HTTP_URL` | `http://localhost:8090` | базовый адрес Push Service; актуальный адрес берётся из `.env` от `tripgoctl` |
| `PUSH_REQUEST_TIMEOUT` | `1s` | таймаут отдельного запроса в Push Service |

Это значения из `.env.example`; `.env`, созданный окружением ЛР3, перекрывает
их при запуске. `PUSH_ADMIN_URL` (`http://localhost:8091` в `.env.example`)
относится к администрированию заглушки и не используется Trip Service. Файл
`.env` и каталог `.tripgo/` в Git не попадают.

## API и ошибки

| Метод | Путь | Успех |
|---|---|---|
| `POST` | `/api/v1/trips` | `201`, заголовок `Location` |
| `GET` | `/api/v1/trips/{tripId}` | `200` |
| `POST` | `/api/v1/trips/{tripId}/finish` | `200` |
| `POST` | `/api/v1/trips/{tripId}/positions` | `201`, сохранённая точка |
| `GET` | `/api/v1/trips/{tripId}/positions` | `200`, объект `{"positions":[...]}` |
| `GET` | `/health` | `200`, без обращения к БД |
| `GET` | `/ready` | `200` или `503`, с ping БД |

Ошибки возвращаются как RFC 9457 (`application/problem+json`) с кодами
`invalid_request`, `trip_not_found`, `trip_completed`, `driver_busy` и
`internal_error`. SQL, DSN и текст внутренних ошибок в ответ не попадают.
Лишние поля JSON, пропущенные обязательные поля и неверные UUID дают
`400 invalid_request`. Тело POST точки — `coordinates` (`latitude`, `longitude`)
и `recorded_at`; только `application/json`, максимум 1 МиБ. Точки можно добавлять
лишь в активную поездку: отсутствующая поездка даёт `404 trip_not_found`,
завершённая — `409 trip_completed`. Маршрут доступен и после завершения;
`positions` отсортированы по `recorded_at` (при равенстве — по `id`),
пустой маршрут возвращается как `[]`.

## Архитектура ЛР2 (база ЛР3)

В ЛР2 три операции и первые две миграции сохранились от ЛР1. В ЛР3 к ним
добавлены точки маршрута и фоновый опрос.

```text
HTTP / OpenAPI → httpapi/{create,get,finish} → usecase/trip/{create,get,finish} → domain
                         ↑                                      ↑
                 observability              postgres (через интерфейсы usecase)
cmd/trip-service → container → конфигурация, пул, репозиторий, юзкейсы, HTTP
```

`domain` знает о поездке и её состояниях, но не об HTTP или SQL. Для create,
get и finish есть отдельные пакеты в HTTP и usecase. Каждый потребитель
объявляет нужный ему интерфейс в `contract.go`; рядом лежат тесты и
сгенерированные GoMock-моки. SQL и транзакции находятся в `storage/postgres`,
а HTTP-слой переводит доменные ошибки в RFC 9457.

Пакеты `internal/storage/postgres` и `internal/observability` оставлены отдельно,
а не собраны в `internal/infra`: первый отвечает за PostgreSQL-репозиторий и
транзакции, второй — за телеметрию HTTP и pgx. При этом направление зависимостей
то же: `domain` не знает об инфраструктуре, интерфейсы для БД и транзакций
объявлены в `usecase`, а `postgres` их реализует. Юзкейсы не импортируют `pgx`
или OpenTelemetry; `container` подключает реализации и телеметрию.

`container` собирает пул, репозиторий, юзкейсы и HTTP-обработчики. В `main`
остались запуск сервера, сигналы и остановка. Сгенерированный OpenAPI-адаптер
не содержит бизнес-правил: он передаёт запрос нужному HTTP-действию.

Настройки `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_RESOURCE_ATTRIBUTES` и
`OTEL_TRACES_SAMPLER` приходят из окружения курса. `.env.example` содержит
локальные значения; актуальные адреса покажет `tripgoctl connect`. Если
коллектор недоступен, HTTP продолжает работать. Для проверки возьмите
`trace_id` из JSON-лога, найдите запрос и DB-спаны в Jaeger, затем откройте
метрики в Grafana.

## Опрос устаревших поездок

```text
PostgreSQL: активные поездки → сканер (каждые POSITION_CHECK_INTERVAL)
                                     ↓
                         очередь (POSITION_QUEUE_SIZE)
                                     ↓
                       POSITION_WORKERS обработчиков
                                     ↓ POST /api/v1/push
                                Push Service
```

Сканер выбирает активные поездки, для которых время последней точки
(`last_position_at`), а при её отсутствии время старта (`started_at`), старше
текущего времени на `POSITION_STALE_AFTER`. Завершённые поездки в выборку не
попадают. Первый проход происходит после интервала, не сразу при старте.
На одну поездку одновременно может приходиться только одно задание в очереди
или обработке. Если очередь заполнена, новое задание сбрасывается без
ожидания; пока поездка остаётся устаревшей, следующий проход может выбрать
её снова. Ошибки сканирования и Push Service не останавливают опрос.

Обработчик отправляет в Push Service `POST /api/v1/push`: `request_id` — новый
UUID, `recipient_id` — UUID водителя, `kind` — `REQUEST_POSITION`, `data.trip_id`
— UUID поездки. Ожидается `202 Accepted`; клиент не делает повторов и ограничен
`PUSH_REQUEST_TIMEOUT`. После успешной записи новой точки запрос для поездки
удаляется из очереди или отменяется, если уже выполняется. Время
`last_position_at` не откатывается при поступлении более ранней точки.
Получение команды проверяется через `tripgoctl environment logs push-service --tail 20`:
заглушка пишет `REQUEST_POSITION` и `recipient_id` водителя, но не `trip_id`.

При завершении работы сканер останавливается, затем обработчики дочитывают
очередь. Ожидание ограничено общим `SHUTDOWN_TIMEOUT`: если он истёк,
выполняющиеся запросы отменяются. Соединения и пул БД закрываются после
остановки HTTP-сервера и фоновой работы.

## Принятые решения

### Транзакции

`TxManager.Do` открывает транзакцию и кладёт её в `context.Context`.
Репозиторий берёт из контекста `pgx.Tx`, а вне `Do` использует `pgxpool.Pool`.
Юзкейсам не нужен импорт `pgx` или аргумент с транзакцией. Вложенный `Do`
переиспользует уже открытую транзакцию. При ошибке или panic происходит
rollback, иначе commit.

Оставил стандартный для PostgreSQL `READ COMMITTED`. Частичный индекс и
условный `UPDATE` защищают два конкурентных сценария без `SERIALIZABLE` и
ретраев. Создание поездки вместе с записью `NULL → active` в историю — одна
транзакция; завершение вместе с `active → completed` — другая.

### Не больше одной активной поездки на водителя

Правило обеспечивает частичный уникальный индекс
`trips_one_active_per_driver_uidx ON trips(driver_id) WHERE status = 'active'`.
Если два POST приходят одновременно, один вставит поездку, второй получит
PostgreSQL `23505`. Сервис переведёт её в `driver_busy` и HTTP 409. Проверять
водителя отдельным `SELECT` перед `INSERT` нельзя: между запросами он мог бы
получить другую поездку.

### Конкурентное завершение

Завершение — один условный запрос:

```sql
UPDATE trips
SET status = 'completed', finished_at = $1, updated_at = $1
WHERE id = $2 AND status = 'active'
RETURNING ...;
```

PostgreSQL блокирует строку на время обновления. Первый запрос получает `200`;
второй после разблокировки не находит активную поездку и получает
`409 trip_completed`. `finished_at` остаётся временем первого завершения.

### Graceful shutdown

По `SIGINT` или `SIGTERM` сервис останавливает HTTP и опрос позиций в пределах
`SHUTDOWN_TIMEOUT`. HTTP-запросы и задания в очереди завершаются до истечения
бюджета; затем отменяются выполняющиеся push-запросы и освобождаются ресурсы.
При превышении бюджета сервис пишет ошибку в лог.

## Миграции

`00001_create_trips.sql` создаёт таблицу `trips`, ограничения и индексы.
`00002_create_trip_status_history.sql` добавляет историю статусов с индексом.
`00003_create_trip_positions.sql` создаёт точки маршрута и индекс для поиска
устаревших активных поездок. Все миграции имеют `Down`; Goose откатывает их
в обратном порядке, поэтому внешние ключи не мешают удалить `trips`.

[Проверка телеметрии](<docs/observability.md>) содержит скриншоты Jaeger и
Grafana, Prometheus-запросы и строку лога с тем же `trace_id`, что и в трейсе.

## Тесты

```bash
go test ./...
make test
# Только с настроенной отдельной тестовой PostgreSQL и DATABASE_URL:
make test-integration DATABASE_URL='postgres://...'
# Если сервис запущен: TRIPGO_BASE_URL=http://localhost:8080 make test-integration
go test -race -count=2 ./...
go build ./...
```

Юнит-тесты покрывают правила поездки, координаты, юзкейсы, обработчики,
problem+json, дедупликацию и отмену очереди, ограничения пула (`-race`,
`goleak`), гейтвей, span link и метрики. Интеграционные тесты работают с
PostgreSQL: проверяют атомарность сохранения точки, порядок маршрута,
невозврат к старому `last_position_at`, выборку по давности и план индекса
через `EXPLAIN (ANALYZE)`. Если задать `TRIPGO_BASE_URL`, запускаются и
HTTP-тесты: жизненный цикл поездки с конкурентными запросами, а также POST/GET
точек, валидация и сохранение маршрута после завершения. `make test-integration`
выполняет тесты дважды, чтобы поймать зависимость от данных прошлого прогона.

## Docker (дополнительная часть)

После `tripgoctl connect` и `make migrate` из быстрого запуска соберите образ:

```bash
docker build -t tripgo-trip-service -f deploy/Dockerfile .
```

На Linux запустите с сетью хоста: адрес `localhost` в `DATABASE_URL` и адресе
OTLP-коллектора тогда указывает на ту же машину, что и при `make run`.

```bash
docker run --rm --network host --env-file .env.example --env-file .env tripgo-trip-service
```

На macOS (Docker Desktop) контейнеру нужен адрес хоста `host.docker.internal`:

```bash
docker run --rm --env-file .env.example --env-file .env \
  -e "DATABASE_URL=${DATABASE_URL/localhost/host.docker.internal}" \
  -e "OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_EXPORTER_OTLP_ENDPOINT/localhost/host.docker.internal}" \
  -e "PUSH_HTTP_URL=${PUSH_HTTP_URL/localhost/host.docker.internal}" \
  -p 8080:8080 tripgo-trip-service
```

Команды выполняются в том же Bash-сеансе, где загружены переменные из быстрого
запуска. `.env` перекрывает `.env.example`; на macOS параметры `-e` заменяют
`localhost` в адресах БД, OTLP и Push Service на адрес хоста Docker Desktop. Если
`tripgoctl connect` выдал другие адреса, проверьте, что они доступны из
контейнера.

Образ собирается в два этапа. Финальный слой основан на distroless, не содержит
исходников и Go toolchain и запускается от non-root пользователя. Размер образа
для ЛР2 отдельно не измерялся (для ЛР1 на `linux/arm64` он составлял 19.7 MB).

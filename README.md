# TripGo — лабораторная работа №2

Сервис поездок для курса «Разработка микросервисов на Go». Он создаёт, отдаёт и
завершает поездки, хранит их в PostgreSQL вместе с историей статусов. Есть
`/health`, `/ready` и остановка без обрыва активных запросов.

## Требования

- Go 1.24 или новее;
- `tripgoctl` из репозитория инфраструктуры курса;
- PostgreSQL и observability-коллектор, поднятые через `tripgoctl`;
- `golangci-lint` для `make lint`.

`oapi-codegen` и `goose` закреплены в `go.mod`: команды вызывают их через
`go tool`, поэтому отдельная установка не нужна.

## Быстрый запуск

```bash
tripgoctl cluster start
tripgoctl environment start
tripgoctl connect

# tripgoctl создаёт .env с адресами сервисов. Таймауты берём из .env.example:
# в файле курса их нет.
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

Скопируйте `id` из ответа POST в `TRIP_ID`, затем проверьте чтение и завершение:

```bash
curl -fsS http://localhost:8080/api/v1/trips/$TRIP_ID
curl -fsS -X POST http://localhost:8080/api/v1/trips/$TRIP_ID/finish
```

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

Для миграций нужен `DATABASE_URL`. Файл
`contracts/openapi/trip-service.openapi.yaml` оставлен без изменений:
`oapi-codegen.yaml` по-прежнему генерирует пять операций из ЛР1.

## Как сдавать

[ЛР1](https://github.com/UnusualNick/template/pull/1) отправлена из `homework/01`,
[ЛР2](https://github.com/UnusualNick/template/pull/2) — из `homework/02` в `main`
того же форка. ЛР1 ещё на ревью, поэтому `homework/02` ответвлена от неё. Когда
первую работу примут, нужно обновить основание второго PR; закрывать первый PR
ради этого не надо. Ссылку отдельно отправлять не требуется — ревьюер увидит
PR в форке. Остальные правила сдачи есть в `grading.md` репозитория курса.

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

Пример значений есть в `.env.example`. Файл `.env` и каталог `.tripgo/` в Git
не попадают.

## API и ошибки

| Метод | Путь | Успех |
|---|---|---|
| `POST` | `/api/v1/trips` | `201`, заголовок `Location` |
| `GET` | `/api/v1/trips/{tripId}` | `200` |
| `POST` | `/api/v1/trips/{tripId}/finish` | `200` |
| `GET` | `/health` | `200`, без обращения к БД |
| `GET` | `/ready` | `200` или `503`, с ping БД |

Ошибки возвращаются как RFC 9457 (`application/problem+json`) с кодами
`invalid_request`, `trip_not_found`, `trip_completed`, `driver_busy` и
`internal_error`. SQL, DSN и текст внутренних ошибок в ответ не попадают.
Лишние поля JSON, пропущенные обязательные поля и неверные UUID дают
`400 invalid_request`.

## Архитектура ЛР2

ЛР2 меняет устройство сервиса, а не его API: три операции и миграции остались
от ЛР1.

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

По `SIGINT` или `SIGTERM` сервис вызывает `http.Server.Shutdown` и даёт активным
запросам до `SHUTDOWN_TIMEOUT` на завершение. Затем закрывает пул. Если время
вышло, принудительно закрывает сервер и пишет ошибку в лог.

## Миграции

`00001_create_trips.sql` создаёт таблицу `trips`, ограничения и индексы.
`00002_create_trip_status_history.sql` добавляет историю статусов с индексом.
У обеих миграций есть `Down`. Goose откатывает их в обратном порядке, поэтому
внешний ключ из истории не мешает удалить `trips`.

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

Юнит-тесты покрывают правила поездки, юзкейсы, обработчики, problem+json,
маршруты и телеметрию. Интеграционные тесты работают с PostgreSQL: проверяют
историю статусов, rollback и повторное завершение. Если задать
`TRIPGO_BASE_URL`, запустится и HTTP-тест: 20 одновременных POST одному водителю
должны дать один `201` и 19 ответов `409`; два одновременных finish — один
`200` и один `409`. `make test-integration` выполняет тесты дважды, чтобы
поймать зависимость от данных прошлого прогона.

## Docker (дополнительная часть)

```bash
docker build -t tripgo-trip-service -f deploy/Dockerfile .
docker run --rm --env-file .env.example --env-file .env -p 8080:8080 tripgo-trip-service
```

Сборка двухэтапная; в финальном distroless-образе нет исходников и Go toolchain,
процесс работает не от root. Перед `docker run` проверьте адреса БД и OTLP в
переменных окружения: `localhost` внутри контейнера не указывает на хост.
Размер образа для ЛР2 отдельно не измерялся.

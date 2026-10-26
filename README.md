# TripGo — лабораторная работа №2

HTTP-сервис поездок для курса «Разработка микросервисов на Go». Реализованы
создание, получение и завершение поездки, хранение в PostgreSQL, история смены
статусов, readiness/liveness и корректное завершение процесса.

## Требования

- Go 1.24 или новее;
- `tripgoctl` из репозитория инфраструктуры курса;
- PostgreSQL и observability-коллектор, поднятые через `tripgoctl`;
- `golangci-lint` для `make lint`.

Все Go-инструменты (`oapi-codegen`, `goose`) закреплены в `go.mod` и запускаются
через `go tool`. Глобально их устанавливать не нужно.

## Быстрый запуск

```bash
tripgoctl cluster start
tripgoctl environment start
tripgoctl connect

# environment start создаёт .env с актуальными адресами; .env.example задаёт
# обязательные HTTP-таймауты, которых нет в генерируемом файле курса.
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

Полученный UUID используется в следующих запросах:

```bash
curl -fsS http://localhost:8080/api/v1/trips/$TRIP_ID
curl -fsS -X POST http://localhost:8080/api/v1/trips/$TRIP_ID/finish
```

## Команды

| Команда | Назначение |
|---|---|
| `make generate` | обновить HTTP-типы из OpenAPI и моки юзкейсов через `mockgen` |
| `make migrate` | применить все миграции |
| `make migrate-down` | откатить все миграции |
| `make run` | запустить сервис |
| `make build` | собрать все пакеты |
| `make test` | выполнить unit-тесты с race detector и покрытием |
| `make test-integration` | применить миграции к указанной тестовой БД и дважды выполнить интеграционные тесты |
| `make lint` | запустить `golangci-lint` |

`DATABASE_URL` должен присутствовать в окружении для целей миграций. Контракт
из `contracts/openapi/trip-service.openapi.yaml` не редактируется; генератор
ограничен пятью операциями первой лабораторной через `oapi-codegen.yaml`.

## Как сдавать

Первая работа сдана отдельным Pull Request из ветки `homework/01`. Вторая
работа сдаётся отдельным Pull Request из ветки `homework/02` в `main`
собственного форка. Поскольку первая работа ещё находится на ревью, ветка
`homework/02` создана от текущей `homework/01`; после принятия первой работы
основание PR нужно синхронизировать, не закрывая исходный PR.
Ссылку на PR никуда отправлять не нужно: ревьюер найдёт его внутри форка.
Подробные правила приведены в `grading.md` репозитория курса.

## Конфигурация

Все значения читаются из окружения и проверяются до старта.

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

Полный безопасный пример находится в `.env.example`. Файл `.env` и каталог
`.tripgo/` игнорируются Git.

## API и ошибки

| Метод | Путь | Успех |
|---|---|---|
| `POST` | `/api/v1/trips` | `201`, заголовок `Location` |
| `GET` | `/api/v1/trips/{tripId}` | `200` |
| `POST` | `/api/v1/trips/{tripId}/finish` | `200` |
| `GET` | `/health` | `200`, без обращения к БД |
| `GET` | `/ready` | `200` или `503`, с ping БД |

Ошибки имеют `Content-Type: application/problem+json` и формат RFC 9457. Сервис
возвращает стабильные коды `invalid_request`, `trip_not_found`,
`trip_completed`, `driver_busy` и `internal_error`; SQL, DSN и внутренние детали
в HTTP-ответ не попадают. Неизвестные JSON-поля, отсутствующие обязательные
поля и невалидные UUID отклоняются как `400 invalid_request`.

## Архитектура ЛР2

Внешний HTTP-контракт остался прежним: создание, чтение и завершение поездки.
Новых ручек и миграций в ЛР2 нет. Зависимости направлены внутрь:

```text
HTTP / OpenAPI → httpapi/{create,get,finish} → usecase/trip/{create,get,finish} → domain
                         ↑                                      ↑
                 observability              postgres (через интерфейсы usecase)
cmd/trip-service → container → конфигурация, пул, репозиторий, юзкейсы, HTTP
```

`domain` описывает поездку, правила переходов и ошибки без знания HTTP и SQL.
Каждый юзкейс и каждая HTTP-ручка создания, чтения и завершения живёт в
отдельном пакете и объявляет узкий интерфейс в своём `contract.go` у
потребителя. Моки, созданные `mockgen`, и тесты лежат рядом с действиями;
транзакции и SQL остаются в `storage/postgres`. HTTP преобразует доменные
ошибки в стабильные RFC 9457 problem-ответы в одном месте. Пакет `container` —
единственная точка сборки конкретных зависимостей и создания пула; `main`
отвечает за конфигурацию, сигналы и жизненный цикл сервера. Сгенерированный
OpenAPI-интерфейс остаётся тонким адаптером для трёх ручек и делегирует
каждую соответствующему HTTP-действию.

Стандартные переменные `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_RESOURCE_ATTRIBUTES` и
`OTEL_TRACES_SAMPLER` задаются в `.env` окружением курса. Значения по умолчанию
для локального примера есть в `.env.example`; реальные адреса коллекторов
нужно брать из `tripgoctl connect`. При недоступном коллекторе сам HTTP-сервис
не должен прекращать работу. По одному запросу сопоставляются trace ID в
JSON-логе, серверный/SQL спаны в Jaeger и агрегированные метрики в Grafana.

## Принятые решения

### Транзакции

`TxManager.Do` начинает транзакцию и помещает её в `context.Context`.
Репозиторий получает исполнителя из контекста: внутри `Do` это `pgx.Tx`, вне
него — `pgxpool.Pool`. Поэтому бизнес-слой не импортирует `pgx` и не передаёт
транзакцию аргументами. Вложенный `Do` видит существующую транзакцию и
переиспользует её. Ошибка и panic приводят к rollback; успешная функция — к
commit.

Выбран `READ COMMITTED`, стандартный уровень PostgreSQL. Инварианты, требующие
сериализации, закреплены атомарными SQL-операциями и ограничениями БД, поэтому
для них не требуется более дорогой `SERIALIZABLE` с обязательными ретраями.

Создание поездки и запись `NULL → active` в `trip_status_history` происходят в
одном `Do`. Завершение и запись `active → completed` также атомарны.

### Две активные поездки одного водителя

Частичный уникальный индекс
`trips_one_active_per_driver_uidx ON trips(driver_id) WHERE status = 'active'`
делает правило безопасным при параллельных запросах. Ошибка PostgreSQL `23505`
по этому constraint преобразуется в доменную `driver_busy`, затем в HTTP 409.
Проверки `SELECT` перед `INSERT` нет: она оставляла бы race window.

### Конкурентное завершение

Завершение выполняется одним условным запросом:

```sql
UPDATE trips
SET status = 'completed', finished_at = $1, updated_at = $1
WHERE id = $2 AND status = 'active'
RETURNING ...;
```

PostgreSQL блокирует строку. Из двух параллельных запросов один получает строку
и `200`; второй после снятия блокировки видит уже `completed` и получает
`409 trip_completed`. `finished_at` повторно не записывается.

### Graceful shutdown

Процесс слушает `SIGINT` и `SIGTERM`, вызывает `http.Server.Shutdown`, ждёт
активные запросы не дольше `SHUTDOWN_TIMEOUT` и только затем закрывает пул. При
исчерпании бюджета выполняется принудительное `Close` и ошибка попадает в лог.

## Миграции

`00001_create_trips.sql` создаёт `trips`, проверки, индекс выборки и частичный
уникальный индекс. `00002_create_trip_status_history.sql` создаёт журнал и его
индекс. У обеих миграций есть рабочий раздел `Down`; goose откатывает их в
обратном порядке, поэтому внешний ключ не мешает удалению `trips`.

Скриншоты настоящего course-infra окружения, Prometheus-запросы и сверка
`trace_id` с JSON-логом находятся в [проверке телеметрии](<docs/observability.md>).

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

Юнит-тесты проверяют конфигурацию, валидацию тела и обязательных полей,
OpenAPI-роутинг, формат problem+json, readiness, создание поездки с историей,
rollback/panic и переиспользование транзакции при вложенном `Do`.

Для полной ручной проверки с окружением курса дополнительно выполняются `up` и
`down` миграций, 20 параллельных `POST` на одного водителя и два параллельных
`finish`: ожидаются соответственно один `201` и девятнадцать `409`, затем один
`200` и один `409`.

## Docker (дополнительная часть)

```bash
docker build -t tripgo-trip-service -f deploy/Dockerfile .
docker run --rm --env-file .env -p 8080:8080 tripgo-trip-service
```

Образ собирается в два этапа. Финальный слой основан на distroless, не содержит
исходников и Go toolchain и запускается от non-root пользователя.
Размер итогового образа — 19.7 MB при сборке для `linux/arm64`.

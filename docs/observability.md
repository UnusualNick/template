# Проверка наблюдаемости в окружении курса (ЛР2)

Проверяли с `tripgoctl v0.1.2`: в `environment.toml` указаны `lab = 2` и
компоненты `postgres`, `observability`. Поды PostgreSQL, OTel Collector, Jaeger,
Prometheus, Grafana и Loki были в статусе Ready. Сервис запустили с
`.env.example` и файлом `.env`, созданным `tripgoctl`. Затем выполнили
HTTP-сценарии и повторно запустили `make test-integration` с `TRIPGO_BASE_URL`.

## Трейс запроса

POST `/api/v1/trips` вернул `201`. В [Jaeger](<screenshots/jaeger-create-trace.png>)
у этого запроса 6 спанов: HTTP-запрос, получение соединения из пула,
`BEGIN`, вставки в `trips` и `trip_status_history`, `COMMIT`. На
[скриншоте DB-спана](<screenshots/jaeger-db-attributes.png>) видны
`db.operation.name=INSERT` и `db.collection.name=trips`, но нет значений SQL.
Полный trace ID: `138897618cae2a35815532e7c17bc98c` (локально:
`http://localhost:22686/trace/138897618cae2a35815532e7c17bc98c`). Строки
stdout для этого запроса:

```json
{"level":"INFO","msg":"trip created","service":"trip-service","trace_id":"138897618cae2a35815532e7c17bc98c","span_id":"68cb13bc305d6b67"}
{"level":"INFO","msg":"http request","service":"trip-service","http.request.method":"POST","http.route":"/api/v1/trips","http.response.status_code":201,"duration_ms":1.636123,"trace_id":"138897618cae2a35815532e7c17bc98c","span_id":"68cb13bc305d6b67"}
```

В исходных строках stdout есть и поле `time`; здесь оно опущено. ID поездки и
тела запроса в событиях нет. Отдельно проверили через Jaeger API GET и finish с
UUID в пути: UUID не попал в имена, атрибуты или события экспортированных
спанов. Лог и трейс сопоставлены по `trace_id`, не по времени.

## Метрики

[Счётчик `tripgo_trips_created_total`](<screenshots/grafana-trips-created.png>)
вырос после создания поездок. Для
[средней длительности POST](<screenshots/grafana-http-duration.png>) использовали
такой запрос в Grafana Explore:

```promql
http_server_request_duration_seconds_sum{http_route="/api/v1/trips"} / http_server_request_duration_seconds_count{http_route="/api/v1/trips"}
```

Prometheus вернул отдельные ряды для `201` и `409`. В метке маршрута — шаблон,
не UUID. В том же окружении прошли `/health`, `/ready`, тесты с PostgreSQL и
параллельные create/finish.

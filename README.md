# Рапределенный вычислитель


## Содержание
- [Общее описание решения](#solution-discriber)
- [Диспетчер](#dispatcher)
    - [Настройки Диспетчера](#dispatcher-settings)
    - [Запуск Диспетчера](#dispatcher-start)
    - [Запросы на вычисление выражений](#dispatcher-query-post)
    - [Запросы информации о ходе и результатах вычислений](#dispatcher-query-get)
- [Хранилище](#storage)
     - [Настройка Хранилища](#storage-start)
- [Менеджер](#manager)
    - [Настройки Менеджера](#manager-settings)
    - [Запуск Менеджера](#manager-start)
    - [Взаимодействие с Демоном(вычислителем)](#manager-daemon)
    - [ВЗаимодействие с Frontend](#manager-frontend)
- [Frontend](#frontend)
- [Todo](#todo)

<a name="solution-discriber"><h2>Общее описание решения</h2></a>
Распределенный вычислитель представляет собой несколько независимых взаимодействующих сервисых компонент:
- Диспетчер - выполняет приём от клиентов запросов на вычисления, регистрацию заданий на вычисления, а также предоставляет клиентам информацию о результатах вычислений. Регистрация заданий на вычисления, а также получение информации о результатах расчетов производятся посредством выполнения запросов к Хранилищу. 
- Хранилище - промежуточное звено между Диспетчером и Менеджером, предназначенное для регистрации выражений на вычисления, хранения и обработки заданий на вычисление выражений.
- Менеджер - выполняет функции оркестрации заданий на вычисления выражений, посредством запросов взаимодействует с Демоном(вычислителем), управляет результатами вычислений.
- Демон(вычислитель) - получает задания на вычисления от Менеджера, выполняет вычисление арифметических выражений и возвращает результаты(Менеджеру)

Для взамодействия с вычислителем, в качестве клиентского приложения,  предлагается используется специальную HTML страницу(см. [Frontend](#frontend)), однако вы вольны использовать любой другой инструмент(Curl, Postman и т.п.).


<a name="dispatcher"><h2>Диспетчер</h2></a>
Диспетчер обеспечивает приём от клиентов запросов на вычисления, регистрацию заданий на вычисления, а также предоставляет клиентам информацию о результатах вычислений. Регистрация заданий на вычисления, а также получение информации о результатах расчетов производятся посредством выполнения запросов к Хранилищу.

Приём входящих клиентских запросов реализован через специально созданный интерфейс HTTP API.
- Для приёма клиентских запросов на вычисления выражений(с последующей их регистрацией в Хранилище), используются [Запросы на вычисление выражений](#dispatcher-query-post).
- Для приёма клиентских запросов информации о ходе и результатах вычислений, используются [Запросы информации о ходе и результатах вычислений](#dispatcher-query-get).


Дополнительные замечания:
- Обработка обоих видов запросов реализована в рамках одного потока главной программы. 
- Диспетчер взаимодействует с Хранилищем(СУБД PostgeSQL) посредством библиотеки "github.com/lib/pq"   
- Диспетчер использует только одно соединение с Хранилищем, которое инициализируется при старте(Диспетчера). Поэтому, перед запуском Диспетчера, необходимо обеспечить доступность Хранилища. Далее, в ходе работы Диспетчера, Хранилище можно выключать и включать(при включенном и доступном Хранилище, Диспетчер сможет с ним взаимодействовать).
- Диспетчер сохраняет работоспособность, независимо от Хранилища и других компонент.  


<a name="dispatcher-settings"><h3>Настройки Диспетчера</h3></a>
Пакет Диспетчер расположен в:  /cmd/dispatcher/main.go

Настройки параметров работы Диспетчера представлены в файле "dispatcher_config.json".
- dispatcherEndpoint - т.н. endpoint, т.е. адрес ресурса, через который построено взаимодействие с Диспетчером
- serverAddress - адрес и порт, на котором работает сервис
- maxLenghtRequestId - максимальная длина идентификатора запроса на регистрацию(вычисление) нового выражения 
- usePrepareValidation - флаг выполнения вализации входящих выражений. Если значение настройки - true, тогда выражения будут проходить предварительную валидацию(см. настройку prepareValidationRegex)
- prepareValidationRegex - строка регулярного выражения, используемая при валидации входящих выражений(см. usePrepareValidation). Настройка используется только если usePrepareValidation=true
- maxLenghtExpression - значение максимальной длины выражения(не может быть более 255)
- useLocalRequestIdCache - флаг использования локального кэша идентификаторов зарегистрированных на вычисления выражений. Используется при кэшировании запросов на добавление заданий на вычисление выражений. Если значение настройки - true, тогда перед обращением к Хранилищу, программа будет искать идентификатор соответствующей задачи в кэше. Если идентификатор задания будет найден в этом кэше, то обращения к Хранилищу выполняться не будет, клиенту будет возвращен результат из кэша. Если в кэше не будет найдена запись идентификатора, то программа выполнит запрос к Хранилищу и вернет соответствующий результат клиенту. При этом, если окажется, что задание ещё не зарегистрировано, то в локальный кэш будет добавлена соответствующая запись идентификатора для данного идентификатора запроса.
- useLocalResultCache - флаг использования локального кэша результатов вычислений. Если значение настройки - true, тогда перед соответствующим обращением к Хранилищу, будет произведен поиск результата вычисления по идентификатору вычислительного задания в специальном кэше результатов. Если результат вычисления будет найден в этом кэше, то обращения к Хранилищу выполняться не будет, клиенту будет возвращен результат из кэша. Если в кэше не будет найдена запись результата, то программа выполнит запрос к Хранилищу и вернет соответствующий результат клиенту. При этом, если окажется, что вычисление уже завершилось, то в локальный кэш результатов будет добавлена соответствующая запись для данного вычислительного задания.
- dbConnectionConfig - стандартные параметры соединения с Хранилищем(в данной реализации с базой данных)

Пример настроечного файла:
```json
{
    "dispatcherEndpoint": "/expression",
    "serverAddress": "127.0.0.1:8081",
    "maxLenghtRequestId": 50,
    "usePrepareValidation": true,
    "prepareValidationRegex": "^[0-9+\\-*/().^\\s]+$",
    "maxLenghtExpression": 255,
    "useLocalRequestIdCache": true,
    "useLocalResultCache": true,
    "dbConnectionConfig": {
        "host": "127.0.0.1",
        "port": 5432,
        "user": "root",
        "password": "root",
        "dbname": "calcDB"
    }
}
```

<a name="dispatcher-start"><h3>Запуск Диспетчера</h3></a>

```
go run cmd/dispatcher/main.go

```


<a name="dispatcher-query-post"><h3>Запросы на вычисление выражений</h3></a>

Обработка включает несколько последовательных этапов:
- проверка наличия идентификатора запроса в заголовке
- поиск в локальном кэше идентификатора задания ("task_id") по идентификатору запроса(опционально, см. настройку useLocalRequestIdCache)
- первичная валидация выражения(опциональная проверка на допустимые символы, без вычисления выражения и т.п.)
- регистрация выражения в Хранилище "как есть", т.е. в той форме(строкового представления), в каком оно было передано Диспетчеру
- обогащение локального кэша результатов для завершенных вычислений(опционально, см. настройку useLocalRequestIdCache)
- возврат клиенту идентификатора("task_id") задания на вычисление выражения

Запрос постановки на вычисление выражения должен представлять собой POST запрос вида:
```
curl -X POST -H "Content-Type: application/json" -H "X-Request-ID: ваш_идентификатор_запроса" -d '{"expression": "ваше_выражение"}' http://адрес_диспетчера:порт_диспетчера/endpoint_диспетчера

```

Пример запроса и ответа:
```
curl -X POST -H "Content-Type: application/json" -H "X-Request-ID: lsphju9uygwhrjv6ywr" -d '{"expression": "2+2"}' http://localhost:8080/expression

```
```
{"task_id":"8"}
```


<a name="dispatcher-query-post"><h3>Запросы информации о результатах вычислений</h3></a>

Обработка включает несколько последовательных этапов:
- поиск результата по идентификатору задания("task_id") в локальном кэше результатов вычислений(опционально, см. настройку useLocalResultCache)
- поиск результата идентификатору задания("task_id") в Хранилище
- обогащение локального кэша результатов для завершенных вычислений(опционально, см. настройку useLocalResultCache)
- возврат клиенту информации о ходе вычисления, или результата(если вычисление завершено)

Запрос информации о результате вычисления должен представлять собой GET запрос вида:
```
curl http://адрес_диспетчера:порт_диспетчера/endpoint_диспетчера?task_id=task_id
```

- task_id - идентификатор вычислительной задачи, который был присвоен выражению при его регистрации в Хранилище методом POST
- status - состояние вычисления("Waiting" - ожидает вычисления; "In progress" - в процессе вычисления; "Completed" - выычисление завершено) 
- result - результат вычисления. Атрибут возвращается заполненным только если вычисления выражения завершено(т.е. если status = "Completed"). Если вычисление не завершено, то атрибут будет без значения.
Атрибуты status и result возвращаются только при успешном выполнении запроса.

Пример запроса и ответа:
```
~$ curl "http://localhost:8081/expression?task_id=129"
```
```
{
    "expression": "10+2.5+15+15/3",
    "result": "0",
    "status": "error",
    "is_finished": true,
    "is_wrong": true,
    "calc_duration": 21814,
    "comment": "The allowed calculation timeout has been exceeded"
}
```

<a name="storage"><h2>Хранилище</h2></a>
Хранилище предназначено для регистрации выражений на вычисления, хранения и обработки заданий на вычисление выражений. 

Хранилище реализовано на базе СУБД PostgeSQL и состоит из двух таблиц, которые описываются своими скриптами создания:

1. reg_expr - основная таблица регистрации заданий на вычисления

```
-- Generated by the database client.
CREATE TABLE reg_expr(
    id SERIAL NOT NULL,
    request_id varchar(50) NOT NULL,
    reg_date timestamp without time zone NOT NULL,
    expression varchar(255) NOT NULL,
    result double precision DEFAULT 0.00,
    status varchar(50) NOT NULL DEFAULT 'new'::character varying,
    is_finished boolean NOT NULL DEFAULT false,
    finish_date timestamp without time zone,
    is_wrong boolean DEFAULT false,
    calc_duration integer NOT NULL DEFAULT 0,
    comment varchar(255),
    PRIMARY KEY(id)
);
CREATE INDEX idx_reg_expr_request_id ON reg_expr USING hash ("request_id");
CREATE INDEX idx_reg_expr_reg_date ON reg_expr USING btree ("reg_date");
COMMENT ON COLUMN reg_expr.request_id IS 'Идентификатор запроса на вычисление выражения';
COMMENT ON COLUMN reg_expr.reg_date IS 'Дата и время регистрации выражения на вычисление';
COMMENT ON COLUMN reg_expr.expression IS 'Выражение для вычисления';
COMMENT ON COLUMN reg_expr.result IS 'Результат вычисления выражения';
COMMENT ON COLUMN reg_expr.status IS 'Краткое описание: new, success, cancel, error';
COMMENT ON COLUMN reg_expr.is_finished IS 'Статус завершения вычисления выражения';
COMMENT ON COLUMN reg_expr.finish_date IS 'Дата и время завершения обработки вычисления';
COMMENT ON COLUMN reg_expr.is_wrong IS 'Флаг ошибки';
COMMENT ON COLUMN reg_expr.calc_duration IS 'Время обработки на вычислителе(в секундах)';
COMMENT ON COLUMN reg_expr.comment IS 'Комментарий';
```

2. calc_expr - таблица Менеджера(очередь Менеджера) для управления вычислительными заданиями
```
-- Generated by the database client.
CREATE TABLE calc_expr(
    id SERIAL NOT NULL,
    task_id integer NOT NULL,
    create_date timestamp without time zone NOT NULL,
    expression varchar(255) NOT NULL,
    result double precision DEFAULT 0.00,
    is_finished boolean NOT NULL DEFAULT false,
    is_wrong boolean NOT NULL DEFAULT false,
    finish_date timestamp without time zone,
    rpn_expression varchar(255),
    comment varchar(255),
    "condition" integer NOT NULL DEFAULT 0,
    PRIMARY KEY(id)
);
COMMENT ON COLUMN calc_expr.task_id IS 'Идентификатор вычислительной задачи';
COMMENT ON COLUMN calc_expr.create_date IS 'Дата и время добавления в очередь  менеджера';
COMMENT ON COLUMN calc_expr.expression IS 'Выражение';
COMMENT ON COLUMN calc_expr.result IS 'Результат вычисления';
COMMENT ON COLUMN calc_expr.is_finished IS 'Флаг окончания обработки sub_expression';
COMMENT ON COLUMN calc_expr.is_wrong IS 'Флаг ошибки вычисления sub_expression';
COMMENT ON COLUMN calc_expr.finish_date IS 'Дата и время финализации';
COMMENT ON COLUMN calc_expr.rpn_expression IS 'Исходное выражение в нотации RPN';
COMMENT ON COLUMN calc_expr.comment IS 'Комментарий Менеджера-вычислителя';
COMMENT ON COLUMN calc_expr."condition" IS 'Состояние процесса вычисления';
```

<a name="storage-start"><h3>Настройка хранилища</h3></a>
В проекте использован докер-образ данного сервера СУБД - см.настройки файла docker-compose.yaml. Для доступа к настройкам и управлению СУБД использовался образ pgadmin, поэтому он также прописан в docker-compose.yaml.

При использовании docker-compose, вам необходимо скачать и инициализировать образы и настройки из файла docker-compose.yaml.
```
sudo docker compose up --build
```

Если вы не используете docker, то вам необходимо создать в доступном вам экземпляре PostgreSQL создать новую базу данных и назвать её calcDB(не забудьте прописать актуальные параметры подключения в настроечных файлах пакетов).

После инициализации СУБД, вам необходимо создать таблицы из предложенных в предыдущем пункте таблиц.  

Для администрирования СУБД, предлагается использовать pgadmin, либо любой другой инструмент, плагин или расширение. 


<a name="manager"><h2>Менеджер</h2></a>
Менеджер предназначен для оркестрации заданий на вычисления выражений, посредством запросов взаимодействует с Демоном(вычислителем), управляет результатами вычислений.



Дополнительные замечания:

    Обработка обоих видов запросов реализована в рамках одного потока главной программы.
    Диспетчер использует только одно соединение с Хранилищем, которое инициализируется при старте(Диспетчера). Поэтому, перед запуском Диспетчера, необходимо обеспечить доступность Хранилища. Далее, в ходе работы Диспетчера, Хранилище можно выключать и включать(при включенном и доступном Хранилище, Диспетчер сможет с ним взаимодействовать).
    Диспетчер сохраняет работоспособность, независимо от Хранилища и других компонент.




<a name="manager-settings"><h3>Настройки Менеджера</h3></a>


   
<a name="manager-start"><h3>Запуск Менеджера</h3></a>

```
go run cmd/manager/main.go

```

<a name="manager-daemon"><h3>Взаимодействие с Демоном(вычислителем)</h3></a>

<a name="manager-frontend"><h3>Взаимодействие с Frontend</h3></a>




<a name="frontend"><h2>Frontend</h2></a>
В качестве клиентского приложения предлагается воспользоваться файлом: /fronend/calc_front.html

Максимальная длина выражения - 255 символов

<!-- описание работы с приложением -->

<a name="todo"><h2>Todo</h2></a>

<!-- описание возможных улучшений-->
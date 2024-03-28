package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

var requestIdCache sync.Map // Кэш RequestID
var resultCache sync.Map    // Кэш готовых результатов

// Структура атрибутов задачи для возврата клиенту
type TaskInfoStruct struct {
	Expression   string `json:"expression"`
	Result       string `json:"result"`
	Status       string `json:"status"`
	IsFinished   bool   `json:"is_finished"`
	IsWrong      bool   `json:"is_wrong"`
	CalcDuration int64  `json:"calc_duration"`
	Comment      string `json:"comment"`
}

// Структура настроек диспетчера
type Config struct {
	DispatcherEndpoint     string `json:"dispatcherEndpoint"`
	ServerAddress          string `json:"serverAddress"`
	MaxLenghtRequestId     int    `json:"maxLenghtRequestId"`
	UsePrepareValidation   bool   `json:"usePrepareValidation"`
	PrepareValidationRegex string `json:"prepareValidationRegex"`
	MaxLenghtExpression    int    `json:"maxLenghtExpression"`
	UseLocalRequestIdCache bool   `json:"useLocalRequestIdCache"`
	UseLocalResultCache    bool   `json:"useLocalResultCache"`
	DBConnectionConfig     struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		DBName   string `json:"dbName"`
	} `json:"dbConnectionConfig"`
}

// Структура входящего запроса(тела) с выражением для вычисления
type ExpressionMessage struct {
	Expression string `json:"expression"`
}

func createDBConnection(config Config) (*sql.DB, error) {
	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		config.DBConnectionConfig.Host,
		config.DBConnectionConfig.Port,
		config.DBConnectionConfig.User,
		config.DBConnectionConfig.Password,
		config.DBConnectionConfig.DBName,
	)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func isCorrectRequestID(w http.ResponseWriter, requestID string, config Config) bool {
	if requestID == "" {
		http.Error(w, "X-Request-ID is missing or empty", http.StatusBadRequest)
		return false
	}

	if config.MaxLenghtRequestId > 0 && (len(requestID) > config.MaxLenghtRequestId) {
		http.Error(w, fmt.Sprintf("X-Request-ID exceeds the maximum length of %d characters", config.MaxLenghtRequestId), http.StatusBadRequest)
		return false
	}

	return true
}

func searchTaskIdByRequestId(w http.ResponseWriter, requestID string, config Config, db *sql.DB) (string, error) {
	// Ищем в локальном кэше
	if config.UseLocalRequestIdCache {
		if cachedTaskID, ok := requestIdCache.Load(requestID); ok {
			if cachedTaskID != "" {
				return cachedTaskID.(string), nil
			} else {
				requestIdCache.Delete(requestID)
			}
		}
	}

	// В кэше не нашли, ищем в БД
	taskID, err := searchRequestInDB(db, requestID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError) // Общая ошибка
		log.Printf("Error getting result from database(searchRequestInDB): %v", err)
		return "", err
	}

	// Если нашли, добавляем в локальный кэш
	if config.UseLocalRequestIdCache && taskID != "" {
		if _, ok := requestIdCache.Load(requestID); ok {
		} else {
			requestIdCache.Store(requestID, taskID)
		}
	}

	return taskID, nil
}

func searchRequestInDB(db *sql.DB, requestID string) (string, error) {
	var taskID string
	err := db.QueryRow("SELECT id FROM reg_expr WHERE request_id = $1 ORDER BY reg_date DESC LIMIT 1", requestID).Scan(&taskID) //берем последнюю(т.е. недавнюю) запись
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return taskID, nil
}

func getCurrExpression(w http.ResponseWriter, r *http.Request) (string, error) {
	var exprMessage ExpressionMessage

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&exprMessage); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", err
	}

	return strings.TrimSpace(exprMessage.Expression), nil
}

func isCorrectExpression(w http.ResponseWriter, CurrExpression string, config Config) bool {
	if CurrExpression == "" {
		http.Error(w, "Invalid expression", http.StatusBadRequest)
		return false
	}

	if config.MaxLenghtExpression > 0 && (len(CurrExpression) > config.MaxLenghtExpression) {
		http.Error(w, fmt.Sprintf("Expression exceeds the maximum length of %d characters", config.MaxLenghtExpression), http.StatusBadRequest)
		return false
	}

	// Предварительная валидация выражения
	if config.UsePrepareValidation {
		if !isValidArithmeticExpression(CurrExpression, config) {
			http.Error(w, "Invalid arithmetic expression", http.StatusBadRequest)
			return false
		}
	}

	return true
}

func isValidArithmeticExpression(expr string, config Config) bool {
	validChars := regexp.MustCompile(config.PrepareValidationRegex)
	if !validChars.MatchString(expr) {
		return false
	}

	// Проверка корректности скобок
	bracketBalance := 0
	for _, char := range expr {
		if char == '(' {
			bracketBalance++
		} else if char == ')' {
			bracketBalance--
			if bracketBalance < 0 {
				return false
			}
		}
	}

	return bracketBalance == 0
}

func pushToStorage(w http.ResponseWriter, r *http.Request, config Config, requestID string, CurrExpression string, db *sql.DB) (string, error) {

	// Записываем(регистрируем) новую задачу в БД
	taskID, err := insertExpressionToDB(db, requestID, CurrExpression)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError) // Общая ошибка
		log.Printf("Error getting result from database(insertExpressionToDB): %v", err)
		return "", err
	}

	// Добавляем в локальный кэш
	if config.UseLocalRequestIdCache && taskID != "" {
		if _, ok := requestIdCache.Load(requestID); ok {
		} else {
			requestIdCache.Store(requestID, taskID)
		}
	}

	return taskID, nil
}

func insertExpressionToDB(db *sql.DB, requestID string, CurrExpression string) (string, error) {
	var taskID string
	err := db.QueryRow("INSERT INTO reg_expr(request_id, reg_date, expression) VALUES($1, $2, $3) RETURNING id", requestID, time.Now(), CurrExpression).Scan(&taskID)
	if err != nil {
		return "", err
	}
	return taskID, nil
}

func returnTaskIdToClient(w http.ResponseWriter, taskID string) {
	response := map[string]string{"task_id": taskID}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

func processPost(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))

	// Валидируем requestID
	if !isCorrectRequestID(w, requestID, config) {
		log.Println("Invalid requestID")
		return
	}

	// Ищем таску(обеспечиваем идемпотентность) в кэше и БД
	taskID, err := searchTaskIdByRequestId(w, requestID, config, db)
	if err != nil {
		log.Println("Error searching taskID by requestID", err)
		return
	}

	if taskID != "" {
		// Нашли таску, возвращаем клиенту taskID(обеспечиваем идемпотентность)
		// returnTaskID(w, taskID)
		returnTaskIdToClient(w, taskID)
		return
	}

	// Не нашли taskID по requestID, поэтому считаем, что это новый запрос на вычисление

	// Извлекаем выражение
	CurrExpression, err := getCurrExpression(w, r)
	if err != nil {
		log.Println("Error getting an expression from an incoming request", err)
		return
	}

	// Проверка корректности полученного выражения
	if !isCorrectExpression(w, CurrExpression, config) {
		log.Println("Invalid incoming expression")
		return
	}

	taskID, err = pushToStorage(w, r, config, requestID, CurrExpression, db)
	if err != nil {
		log.Println("Error adding new expression in storage", err)
		return
	}

	returnTaskIdToClient(w, taskID)
}

func getResultInfoFromDatabase(db *sql.DB, taskID string) (*TaskInfoStruct, error) {
	var TaskInfo TaskInfoStruct
	err := db.QueryRow(`
		SELECT 
			expression, 
			result, 
			COALESCE(status, '') AS status,
			is_finished,
			is_wrong,
			calc_duration,
			COALESCE(comment, '') AS comment
		FROM reg_expr 
		WHERE id = $1
	`, taskID).Scan(
		&TaskInfo.Expression,
		&TaskInfo.Result,
		&TaskInfo.Status,
		&TaskInfo.IsFinished,
		&TaskInfo.IsWrong,
		&TaskInfo.CalcDuration,
		&TaskInfo.Comment,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("record not found for task_id: " + taskID)
		}
		return nil, err
	}
	return &TaskInfo, nil
}

func isValidTaskID(w http.ResponseWriter, taskID string) bool {
	if taskID == "" {
		http.Error(w, "task_id is missing or empty", http.StatusBadRequest)
		return false
	}

	// Попытка преобразовать taskID в число
	taskIDInt, err := strconv.Atoi(taskID)
	if err != nil {
		http.Error(w, "task_id is missing", http.StatusBadRequest)
		return false
	}

	// Проверка, что строковое представление преобразованного числа совпадает с исходной строкой
	if strconv.Itoa(taskIDInt) != taskID {
		http.Error(w, "task_id is missing", http.StatusBadRequest)
		return false
	}

	return true
}

func returnTaskInfoToClient(w http.ResponseWriter, TaskInfo *TaskInfoStruct) {
	responseJSON, err := json.Marshal(TaskInfo)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

func processGet(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))

	// Валидируем taskID
	if !isValidTaskID(w, taskID) {
		log.Println("Empty or missing task_id")
		return
	}

	// Ищем информацию по задаче в кэше готовых результатов(где IsFinished=true)
	if config.UseLocalResultCache {
		if cachedTaskInfo, ok := resultCache.Load(taskID); ok {
			// Нашли - возвращаем клиенту
			returnTaskInfoToClient(w, cachedTaskInfo.(*TaskInfoStruct))
			return
		}
	}

	// В кэше не нашли, получим из БД
	TaskInfo, err := getResultInfoFromDatabase(db, taskID)
	if err != nil {
		// Проверяем тип ошибки
		if strings.HasPrefix(err.Error(), "Record not found for task_id") {
			// Ошибка клиента
			http.Error(w, fmt.Sprintf("%v", err), http.StatusBadRequest)
		} else {
			// Ошибка сервера
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		log.Printf("Error getting result from database(getResultFromDatabase): %v", err)
		return
	}

	// Добавим в локальный кэш
	if config.UseLocalResultCache && TaskInfo.IsFinished {
		if _, ok := resultCache.Load(taskID); ok {
		} else {
			resultCache.Store(taskID, TaskInfo)
		}
	}

	returnTaskInfoToClient(w, TaskInfo)
}

func processIncomingRequest(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {
	switch r.Method {
	case "POST":
		processPost(w, r, config, db)
	case "GET":
		processGet(w, r, config, db)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		log.Printf("Method Not Allowed: %s", r.Method)
		return
	}
}

func loadConfig(filename string) (Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func main() {

	// Настройки приложения
	config, err := loadConfig("dispatcher_config.json")
	if err != nil {
		log.Println("Error loading config (dispatcher_config.json):", err)
		return
	}

	// Создание подключения к базе данных
	db, err := createDBConnection(config)
	if err != nil {
		log.Println("Error creating DB connection:", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Println("Error closing DB connection:", err)
		}
	}()

	// Хендлер-обработчик входящих запросов
	http.HandleFunc(config.DispatcherEndpoint, func(w http.ResponseWriter, r *http.Request) {
		// Добавляем заголовки CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

		// Проверяем метод запроса, и если это OPTIONS, завершаем обработку
		if r.Method == "OPTIONS" {
			return
		}

		processIncomingRequest(w, r, config, db)
	})

	log.Println("Server 'Dispatcher' is listening on", config.ServerAddress)
	log.Fatal(http.ListenAndServe(config.ServerAddress, nil))
}

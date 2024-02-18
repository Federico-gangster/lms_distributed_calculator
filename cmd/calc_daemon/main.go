package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"ya_lms_expression_calc/internal/expression_processing"
)

// var calcQueue sync.Map  // Кэш вычислителя
// var taskStatus sync.Map // Карта состояния задач
var (
	calcQueue                 sync.Map // Кэш вычислителя
	taskStatus                sync.Map // Карта состояния задач
	lastRunTime               time.Time
	qntProcessedTasks         int
	summaryTimeProcessedTasks int
)

const (
	queued    = "queued"    // Задача в очереди(свободна)
	running   = "running"   // Задача обрабатывается
	completed = "completed" // Задача завершена
)

type Config struct {
	DaemonEndpoint                                string `json:"calcdaemonEndpoint"`
	UpdateConfigEndpoint                          string `json:"updateConfigEndpoint"`
	ServerAddress                                 string `json:"serverAddress"`
	ManagerEndpoint                               string `json:"managerEndpoint"`
	ManagerServerAddress                          string `json:"managerServerAddress"`
	MaxLenghtCalcQueue                            int    `json:"maxLenghtCalcQueue"`
	ProcessingDelaySeconds                        int    `json:"processingDelaySeconds"`
	MaxGoroutinesInCalcProcess                    int    `json:"maxGoroutinesInCalcProcess"`
	AdditionalDelayInCalculationOperationSeconds  int    `json:"additionalDelayInCalculationOperationSeconds"`
	MainLoopDelaySeconds                          int    `json:"mainLoopDelaySeconds"`
	AuxiliaryLoopDelaySeconds                     int    `json:"auxiliaryLoopDelaySeconds"`
	SendIntermediateRPNExpressionToManager        bool   `json:"sendIntermediateRPNExpressionToManager"`
	DeleteFinishedTasksAfterTransferringToManager bool   `json:"deleteFinishedTasksAfterTransferringToManager"`
}

type Task struct {
	TaskID           int     `json:"task_id"`
	Expression       string  `json:"expression"`
	RPNexpression    string  `json:"rpn_expression"`
	RegDateTime      string  `json:"registration_date"`
	CalcDuration     int     `json:"calc_duration"`
	IsWrong          bool    `json:"is_wrong"`
	IsFinished       bool    `json:"is_finished"`
	Comment          string  `json:"comment"`
	Result           float64 `json:"result"`
	TaskDaemonStatus string  `json:"taskDaemonStatus"`
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

func isValidTaskID(w http.ResponseWriter, taskID int) bool {
	return taskID > 0
}

func getTaskFromCache(taskID int) (*Task, bool) {
	cachedTask, ok := calcQueue.Load(taskID)
	if !ok {
		return nil, false
	}

	task, ok := cachedTask.(*Task)
	if !ok {
		return nil, false
	}

	return task, true
}

func addTaskToCache(w http.ResponseWriter, TaskID int, task *Task) {
	qntProcessedTasks++

	task.IsWrong = false
	task.IsFinished = false
	task.RegDateTime = time.Now().Format(time.RFC3339)

	calcQueue.Store(TaskID, task)
	initTaskStatus(TaskID)
	returnTaskInformationToClient(w, task)

}

// sync.Map - не предоставляет встроенной функции получения текущей длины
func calcQueueSize() int {
	var size int
	calcQueue.Range(func(_, _ interface{}) bool {
		size++
		return true
	})
	return size
}

func returnTaskInformationToClient(w http.ResponseWriter, task *Task) {
	responseJSON, err := json.Marshal(task)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		log.Printf("Error encoding JSON: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

func processPost(w http.ResponseWriter, r *http.Request, config Config) {

	// Декодируем JSON в структуру задачи
	var task Task
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		http.Error(w, "Error decoding JSON", http.StatusBadRequest)
		log.Printf("Error decoding JSON: %v", err)
		return
	}
	// Закрываем тело запроса
	defer r.Body.Close()

	// Валидируем taskID
	if !isValidTaskID(w, task.TaskID) {
		http.Error(w, "Invalid TaskID", http.StatusBadRequest)
		log.Printf("Invalid TaskID: %d", task.TaskID)
		return
	}

	// Проверяем, есть ли задача в кэше вычислителя
	currTask, found := getTaskFromCache(task.TaskID)
	if found {
		returnTaskInformationToClient(w, currTask)
	} else {
		if config.MaxLenghtCalcQueue > calcQueueSize() {
			addTaskToCache(w, task.TaskID, &task)
		} else {
			// Нет места в очереди вычислителя, попробуйте позже
			http.Error(w, "No available slots in the queue. Please try again later.", http.StatusServiceUnavailable)
		}
	}
}

func processGet(w http.ResponseWriter, r *http.Request, config Config) {

	taskIDStr := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskIDStr != "" {
		taskID, err := strconv.Atoi(taskIDStr)
		if err != nil {
			http.Error(w, "Invalid TaskID", http.StatusBadRequest)
			log.Printf("Invalid TaskID: %s", taskIDStr)
			return
		}

		if taskID <= 0 {
			http.Error(w, "Invalid TaskID", http.StatusBadRequest)
			log.Printf("Invalid TaskID: %d", taskID)
			return
		}

		// mValue := make(map[string]string)
		cachedTask, ok := calcQueue.Load(taskID)
		if ok {
			task, ok := cachedTask.(*Task)
			if ok {
				status, ok := taskStatus.Load(taskID)
				if ok {
					task.TaskDaemonStatus = status.(string)
				}
				// mValue["task_id"] = taskIDStr
				// mValue["is_finished"] = strconv.FormatBool(task.IsFinished)
				// mValue["is_wrong"] = strconv.FormatBool(task.IsWrong)
			}
			returnTaskInformationToClient(w, task)

		} else {
			http.Error(w, "Not found TaskID", http.StatusBadRequest)
			log.Printf("Not found  TaskID: %d", taskID)
		}

	} else {

		// Возвращаем настройки и список всех задач
		mapInfo := make(map[string]interface{})

		var mu sync.Mutex
		mTaskList := make(map[int]map[string]string)

		calcQueue.Range(func(key, value interface{}) bool {
			taskID := key.(int)
			task := value.(*Task)

			mValue := make(map[string]string)
			mValue["expression"] = task.Expression
			mValue["rpn_expression"] = task.RPNexpression
			mValue["is_finished"] = strconv.FormatBool(task.IsFinished)
			mValue["is_wrong"] = strconv.FormatBool(task.IsWrong)
			mValue["result"] = strconv.FormatFloat(task.Result, 'f', -1, 64)

			status, ok := taskStatus.Load(taskID)
			if !ok {
				mValue["daemon_status"] = ""
			} else {
				mValue["daemon_status"] = status.(string)
			}

			mu.Lock()
			mTaskList[taskID] = mValue
			mu.Unlock()

			return true
		})

		mapInfo["last_run_time"] = lastRunTime.Format(time.RFC3339)
		mapInfo["uptime"] = int(time.Since(lastRunTime).Seconds() + 0.5)
		// mapInfo["last_run_processed_tasks"] = lastRunProcessedTasks
		mapInfo["qnt_current_tasks"] = calcQueueSize()

		mapInfo["qnt_processed_tasks"] = qntProcessedTasks

		if qntProcessedTasks > 0 {
			mapInfo["avg_time_per_tasks"] = summaryTimeProcessedTasks / qntProcessedTasks
		} else {
			mapInfo["avg_time_per_tasks"] = 0
		}

		mapInfo["current_tasks"] = mTaskList
		mapInfo["current_settings"] = config

		responseJSON, err := json.Marshal(mapInfo)
		if err != nil {
			http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
			log.Printf("Error encoding JSON: %v", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseJSON)
	}

}

func IncomingRequests(w http.ResponseWriter, r *http.Request, config Config) {
	switch r.Method {
	case "POST":
		processPost(w, r, config)
	case "GET":
		processGet(w, r, config)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		log.Printf("Method Not Allowed: %s", r.Method)
		return
	}
}

func initTaskStatus(taskID int) {
	taskStatus.Store(taskID, queued)
}

func setTaskRunning(taskID int) {
	taskStatus.Store(taskID, running)
}

func setTaskCompleted(taskID int) {
	taskStatus.Store(taskID, completed)
}

func isTaskQueued(taskID int) bool {
	status, ok := taskStatus.Load(taskID)
	if !ok {
		return false
	}
	return status == queued
}

func isOperator(token string) bool {
	if token == "+" || token == "-" || token == "*" || token == "/" || token == "^" {
		return true
	}
	return false
}

type RPNTuple struct {
	TokenA       string
	TokenB       string
	Operation    string
	Result       float64
	IsWrong      bool
	ErrorComment string
}

func executeOperation(config Config, tokenA, tokenB, operation string) (float64, error) {

	time.Sleep(time.Duration(config.AdditionalDelayInCalculationOperationSeconds) * time.Second)

	operandA, errA := strconv.ParseFloat(tokenA, 64)
	operandB, errB := strconv.ParseFloat(tokenB, 64)
	if errA != nil || errB != nil {
		return 0, errors.New("error parsing operands")
	}

	switch operation {
	case "+":
		return operandA + operandB, nil
	case "-":
		return operandA - operandB, nil
	case "*":
		return operandA * operandB, nil
	case "/":
		if operandB == 0 {
			return 0, errors.New("division by zero")
		}
		return operandA / operandB, nil
	case "^":
		return math.Pow(operandA, operandB), nil
	default:
		return 0, errors.New("unknown operation")
	}
}

func execSubExpression(subExpr map[string]RPNTuple, config Config) {
	var wg sync.WaitGroup
	var mutex sync.Mutex
	sem := make(chan struct{}, config.MaxGoroutinesInCalcProcess)

	for key, value := range subExpr {
		sem <- struct{}{} // Захватываем слот в канале (ожидаем, если слоты закончились)

		wg.Add(1)
		go func(key string, value RPNTuple) {
			defer func() {
				<-sem // Освобождаем слот в канале после завершения горутины
				wg.Done()
			}()

			result, err := executeOperation(config, value.TokenA, value.TokenB, value.Operation)
			if err != nil {
				value.IsWrong = true
				value.ErrorComment = err.Error()
			} else {
				value.Result = result
			}

			mutex.Lock()
			subExpr[key] = value
			mutex.Unlock()
		}(key, value)

	}
	wg.Wait()
	close(sem) // Закрываем канал после завершения всех горутин
}

func evaluateRPN(tokens []string, task *Task, config Config) (float64, error) {

	var currSlice []string
	currSubExpr := make(map[string]RPNTuple)
	n := 0

	for _, token := range tokens {
		lenSlice := len(currSlice)

		if lenSlice >= 2 {
			if isOperator(token) && !isOperator(currSlice[lenSlice-1]) && !isOperator(currSlice[lenSlice-2]) {
				currTuple := RPNTuple{currSlice[lenSlice-2], currSlice[lenSlice-1], token, 0, false, ""}

				// Формируем имя нового идентификатора для вставки результата выражения
				idForPaste := "op_" + strconv.Itoa(n)
				currSubExpr[idForPaste] = currTuple
				currSlice[lenSlice-2] = idForPaste
				n++
				currSlice = append(currSlice, token)
				continue
			}

			currSlice = append(currSlice, token)

		} else {
			// если тут есть операторы, то это ошибка
			currSlice = append(currSlice, token)
		}
	}

	if len(currSubExpr) > 0 {
		execSubExpression(currSubExpr, config)

		for key, value := range currSubExpr {
			if value.IsWrong {
				// Дальнейший расчет можно не производить
				return 0, errors.New("error in calculating the operation")
			}

			// Находим индекс элемента в currSlice по ключу
			indexToUpdate := -1
			for i, token := range currSlice {
				if token == key {
					indexToUpdate = i
					break
				}
			}

			if indexToUpdate == -1 {
				// Дальнейший расчет можно не производить
				return 0, errors.New("error in calculating the operation")
			} else {
				// Заменяем текущий элемент в currSlice на value.Result, преобразованный в строку
				currSlice[indexToUpdate] = strconv.FormatFloat(value.Result, 'f', -1, 64)

				// Удаляем следующие справа 2 элемента, если они есть
				if len(currSlice) > indexToUpdate+2 {
					currSlice = append(currSlice[:indexToUpdate+1], currSlice[indexToUpdate+3:]...)
				}
			}
		}
	}

	if len(currSlice) == 1 {
		result, err := strconv.ParseFloat(currSlice[0], 64)
		if err != nil {
			return 0, errors.New("error parsing final result")
		}
		return result, nil
	}

	if len(currSlice) > 0 {
		sendIntermediateExpressionToManager(currSlice, task, config)
		return evaluateRPN(currSlice, task, config)
	}

	return 0, nil
}

func taskProcessing(task *Task, config Config) {
	result, err := evaluateRPN(strings.Fields(task.RPNexpression), task, config)
	if err != nil {
		task.Comment = fmt.Sprintf("Error: %v\n", err)
		task.IsWrong = true
		task.IsFinished = true
	} else {
		task.Result = result
		task.IsWrong = false
		task.IsFinished = true
	}
	regTime, err := time.Parse(time.RFC3339, task.RegDateTime)
	if err == nil {
		task.CalcDuration = int(time.Since(regTime).Seconds() + 0.5)
	}
}

func processTaskInBackground(taskID int, task *Task, config Config) {
	defer setTaskCompleted(taskID)

	// Проверяем наличие выражения
	currExpression := strings.TrimSpace(task.Expression)
	if currExpression == "" {
		log.Printf("Error expression for task_id: %d", task.TaskID)
		task.Comment = fmt.Sprintf("Error expression for task_id: %d", task.TaskID)
		task.IsWrong = true
		task.IsFinished = true

		regTime, err := time.Parse(time.RFC3339, task.RegDateTime)
		if err == nil {
			task.CalcDuration = int(time.Since(regTime).Seconds() + 0.5)
		}

		return
	}

	// Проверяем, не передано ли RPN(если не передано, тогда вычисляем его)
	currRPNExpression := strings.TrimSpace(task.RPNexpression)
	if currRPNExpression == "" {
		postfix, err := expression_processing.Parse(task.Expression)
		if err != nil {
			log.Printf("Error getting RPN for task_id %d: %v", task.TaskID, err)
			task.Comment = fmt.Sprintf("Error getting RPN for task_id: %d", task.TaskID)
			task.IsWrong = true
			task.IsFinished = true

			regTime, err := time.Parse(time.RFC3339, task.RegDateTime)
			if err == nil {
				task.CalcDuration = int(time.Since(regTime).Seconds() + 0.5)
			}

			return
		}

		// Поскольку, в общем случае, у нас вычислитель может быть на другой машине, то
		// передаем RPN именно как строку(хотя конечно уже есть срез postfix, и мы его ещё раз вычислим)
		task.RPNexpression = expression_processing.PostfixToString(postfix)
	}

	// Обработка задачи
	taskProcessing(task, config)
}

func processingTaskQueue(config Config) {
	currentTime := time.Now()

	// Проходим по всем задачам в кэше
	calcQueue.Range(func(key, value interface{}) bool {
		taskID := key.(int)
		task := value.(*Task)

		// Проверяем, нужно ли обработать задачу
		if isTaskQueued(taskID) {
			taskTime, err := time.Parse(time.RFC3339, task.RegDateTime)
			if err != nil {
				log.Printf("Error parsing task registration time for TaskID %d: %v", taskID, err)
				return true
			}

			// Если прошло определенное время с момента регистрации задачи
			if currentTime.Sub(taskTime) > time.Duration(config.ProcessingDelaySeconds)*time.Second {
				setTaskRunning(taskID)

				// Запускаем горутину-обработчик
				go processTaskInBackground(taskID, task, config)
			}
		}

		return true
	})
}

func runCalcDaemonLoop(config Config) {
	for {

		// Обработка очереди задач
		processingTaskQueue(config)

		// Ожидание перед следующей итерацией
		if config.MainLoopDelaySeconds > 0 {
			time.Sleep(time.Duration(config.MainLoopDelaySeconds) * time.Second)
		}

	}
}

func sendIntermediateExpressionToManager(currSlice []string, task *Task, config Config) error {
	// log.Printf("Intermediate expression: %s", currSlice)
	if config.SendIntermediateRPNExpressionToManager {
		data := map[string]interface{}{
			"task_id":           task.TaskID,
			"expression":        task.Expression,
			"rpn_expression":    strings.Join(currSlice, " "), // Промежуточное значение, на вычислителе не храним
			"registration_date": task.RegDateTime,
			"calc_duration":     task.CalcDuration,
			"is_wrong":          task.IsWrong,
			"is_finished":       task.IsFinished,
			"comment":           task.Comment,
			"result":            task.Result,
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			return err
		}

		ManagerEndpoint := config.ManagerServerAddress + config.ManagerEndpoint
		resp, err := http.Post(ManagerEndpoint, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		log.Println("Status sent to Manager successfully")
	}

	return nil
}

func sendTaskInformationToManager(task *Task, config Config) error {

	data := map[string]interface{}{
		"task_id":           task.TaskID,
		"expression":        task.Expression,
		"rpn_expression":    task.RPNexpression,
		"registration_date": task.RegDateTime,
		"calc_duration":     task.CalcDuration,
		"is_wrong":          task.IsWrong,
		"is_finished":       task.IsFinished,
		"comment":           task.Comment,
		"result":            task.Result,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("error marshalling JSON data: %v", err)
	}

	ManagerEndpoint := config.ManagerServerAddress + config.ManagerEndpoint
	resp, err := http.Post(ManagerEndpoint, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		// return err
		return fmt.Errorf("error sending POST request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	log.Println("Status sent to Manager successfully")

	return nil
}

func processTasksInQueue(config Config) {

	calcQueue.Range(func(key, value interface{}) bool {
		taskID := key.(int)
		task := value.(*Task)

		// Проверяем, завершена ли задача
		if task.IsFinished {
			summaryTimeProcessedTasks = summaryTimeProcessedTasks + task.CalcDuration

			// Информируем Менеджера
			if err := sendTaskInformationToManager(task, config); err != nil {
				log.Printf("Error sending task information to Manager: %v", err)
			} else {
				//Успешно отправили
				if config.DeleteFinishedTasksAfterTransferringToManager {
					calcQueue.Delete(taskID)
					taskStatus.Delete(taskID)
					log.Printf("Task %d information sent to Manager successfully", taskID)
				}
			}
		}

		return true
	})
}

// /
// /
// /
func runAuxiliaryLoop(config Config) {
	for {

		processTasksInQueue(config)

		// Ожидание перед следующей итерацией
		if config.AuxiliaryLoopDelaySeconds > 0 {
			time.Sleep(time.Duration(config.AuxiliaryLoopDelaySeconds) * time.Second)
		}

	}
}

type SettingsUpdateRequest struct {
	Settings string `json:"settings"`
}

type SettingsStruct struct {
	MaxLenghtCalcQueue                            int  `json:"maxLenghtCalcQueue"`
	ProcessingDelaySeconds                        int  `json:"processingDelaySeconds"`
	MaxGoroutinesInCalcProcess                    int  `json:"maxGoroutinesInCalcProcess"`
	AdditionalDelayInCalculationOperationSeconds  int  `json:"additionalDelayInCalculationOperationSeconds"`
	MainLoopDelaySeconds                          int  `json:"mainLoopDelaySeconds"`
	AuxiliaryLoopDelaySeconds                     int  `json:"auxiliaryLoopDelaySeconds"`
	SendIntermediateRPNExpressionToManager        bool `json:"sendIntermediateRPNExpressionToManager"`
	DeleteFinishedTasksAfterTransferringToManager bool `json:"deleteFinishedTasksAfterTransferringToManager"`
}

func updateConfig(w http.ResponseWriter, r *http.Request, config *Config) {
	var updateRequest SettingsUpdateRequest
	err := json.NewDecoder(r.Body).Decode(&updateRequest)
	if err != nil {
		http.Error(w, "Error decoding JSON", http.StatusBadRequest)
		log.Printf("Error decoding JSON: %v", err)
		return
	}
	defer r.Body.Close()

	if updateRequest.Settings != "" {
		var settings SettingsStruct
		err := json.Unmarshal([]byte(updateRequest.Settings), &settings)
		if err != nil {
			http.Error(w, "Error decoding settings JSON", http.StatusBadRequest)
			log.Printf("Error decoding settings JSON: %v", err)
			return
		}

		// Обновление текущих настроек в памяти
		config.MaxLenghtCalcQueue = settings.MaxLenghtCalcQueue
		config.ProcessingDelaySeconds = settings.ProcessingDelaySeconds
		config.MaxGoroutinesInCalcProcess = settings.MaxGoroutinesInCalcProcess
		config.AdditionalDelayInCalculationOperationSeconds = settings.AdditionalDelayInCalculationOperationSeconds
		config.MainLoopDelaySeconds = settings.MainLoopDelaySeconds
		config.AuxiliaryLoopDelaySeconds = settings.AuxiliaryLoopDelaySeconds
		config.SendIntermediateRPNExpressionToManager = settings.SendIntermediateRPNExpressionToManager
		config.DeleteFinishedTasksAfterTransferringToManager = settings.DeleteFinishedTasksAfterTransferringToManager

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "Missing 'settings' section in JSON", http.StatusBadRequest)
		log.Println("Missing 'settings' section in JSON")
	}
}

func main() {

	lastRunTime = time.Now()

	// Загрузка настроек приложения
	config, err := loadConfig("calcdaemon_config.json")
	if err != nil {
		log.Println("Error loading config (calcdaemon_config.json):", err)
		return
	}

	// Инициализация HTTP сервера.
	http.HandleFunc(config.DaemonEndpoint, func(w http.ResponseWriter, r *http.Request) {
		// Добавляем заголовки CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

		// Проверяем метод запроса, и если это OPTIONS, завершаем обработку
		if r.Method == "OPTIONS" {
			return
		}

		// Хендлер запросов поступления новых задач
		IncomingRequests(w, r, config)
	})

	http.HandleFunc(config.UpdateConfigEndpoint, func(w http.ResponseWriter, r *http.Request) {
		// Добавляем заголовки CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

		// Проверяем метод запроса, и если это OPTIONS, завершаем обработку
		if r.Method == "OPTIONS" {
			return
		}

		// Хендлер-обработчик для обновления конфигурации
		updateConfig(w, r, &config)
	})

	// Запускаем HTTP сервер в горутине
	go func() {
		log.Println("Server 'calc_daemon' is listening on", config.ServerAddress)
		// log.Fatal(http.ListenAndServe(config.ServerAddress, nil))
		log.Println(http.ListenAndServe(config.ServerAddress, nil))
	}()

	// Запуск основного цикла в горутине(вычисление выражений)
	go runCalcDaemonLoop(config)

	// Запуск вспомогательного цикла в горутине(чистка очереди выполненных, уведомления Менеджеру)
	go runAuxiliaryLoop(config)

	// Бесконечный цикл для того, чтобы не дать программе завершиться
	select {}
}

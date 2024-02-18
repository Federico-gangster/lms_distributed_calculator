package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"ya_lms_expression_calc/internal/expression_processing"

	_ "github.com/lib/pq"
)

type Config struct {
	ManagerEndpoint    string `json:"managerEndpoint"`
	ServerAddress      string `json:"serverAddress"`
	DBConnectionConfig struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		DBName   string `json:"dbName"`
	} `json:"dbConnectionConfig"`
	MainLoopDelaySeconds   int    `json:"mainLoopDelaySeconds"`
	AddnigLoopDelaySeconds int    `json:"addnigLoopDelaySeconds"`
	MaxLenghtManagerQueue  int    `json:"maxLenghtManagerQueue"`
	MaxDurationForTask     int    `json:"maxDurationForTask"`
	MakeRPNInManager       bool   `json:"makeRPNInManager"`
	MaxManagersWorkers     int    `json:"maxManagersWorkers"`
	DaemonEndpoint         string `json:"calcdaemonEndpoint"`
	DaemonServerAddress    string `json:"calcDaemonServerAddress"`
}

type TasksStruct struct {
	RecordID           int     `json:"id"`
	TaskID             int     `json:"task_id"`
	CreateDateTime     string  `json:"create_date"`
	FinishDateTime     string  `json:"finish_date"`
	CalcDuration       int64   `json:"calc_duration"`
	Expression         string  `json:"expression"`
	RPNexpression      string  `json:"rpn_expression"`
	Comment            string  `json:"comment"`
	Status             string  `json:"status"`
	Condition          int     `json:"condition"`
	Result             float64 `json:"result"`
	IsWrong            bool    `json:"is_wrong"`
	IsFinished         bool    `json:"is_finished"`
	IsFinishedInRegTab *bool   `json:"is_finished_regtab"`
	RequestID          string  `json:"request_id"`
	RegDateTime        string  `json:"reg_date"`
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

// /
// /
// /
func isValidTaskID(w http.ResponseWriter, taskID int) bool {
	return taskID > 0
}

func getTaskByTaskID(db *sql.DB, taskID int) (*TasksStruct, error) {
	var task TasksStruct

	row := db.QueryRow(`
		SELECT 
			id,
			task_id,
			expression,
			COALESCE(rpn_expression, '') AS rpn_expression, 
			condition,
			result,
			is_wrong,
			is_finished
		FROM calc_expr
		WHERE 
			task_id = $1
	`, taskID)

	err := row.Scan(
		&task.RecordID,
		&task.TaskID,
		&task.Expression,
		&task.RPNexpression,
		&task.Condition,
		&task.Result,
		&task.IsWrong,
		&task.IsFinished,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Задача не найдена
		}
		return nil, err
	}

	return &task, nil
}

func processPost(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {

	// Декодируем JSON в структуру задачи
	var task TasksStruct
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

	currTask, err := getTaskByTaskID(db, task.TaskID)
	if err != nil {
		http.Error(w, "Error getting task information", http.StatusInternalServerError)
		log.Printf("Error getting task information: %v", err)
		return
	}

	// Если не найдена, считаем, что задача и обработка этого запроса - не актуальны(задача уже не в очереди Менеджера)
	if currTask == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Task not found"))
		log.Printf("Not found task_id: %d", task.TaskID)
		return
	}

	if currTask.IsFinished {
		// Завершенные задачи не трогаем
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Task already finished"))
		log.Printf("Task already finished, task_id: %d", task.TaskID)
		return
	}

	//
	newValues := map[string]interface{}{
		"rpn_expression": task.RPNexpression,
		"is_wrong":       task.IsWrong,
		"is_finished":    task.IsFinished,
		"comment":        task.Comment,
		"result":         task.Result,
		// "result": task.CalcDuration,
	}
	// if currTask.Condition == 2 {
	// 	newValues["condition"] = 1
	// }

	if err := updateTaskAttributes(db, currTask.RecordID, newValues); err != nil {
		log.Printf("Error updating task attributes after processing for task_id %d: %v", task.TaskID, err)
		return
	}

}

// /
type RegisteredTasksForClients struct {
	// RecordID           int     `json:"id"`
	TaskID int `json:"task_id"`
	// CreateDateTime     string  `json:"create_date"`
	FinishDateTime string `json:"finish_date"`
	CalcDuration   int64  `json:"calc_duration"`
	Expression     string `json:"expression"`
	// RPNexpression      string  `json:"rpn_expression"`
	Comment string `json:"comment"`
	Status  string `json:"status"`
	// Condition          int     `json:"condition"`
	Result     float64 `json:"result"`
	IsWrong    bool    `json:"is_wrong"`
	IsFinished bool    `json:"is_finished"`
	// IsFinishedInRegTab *bool   `json:"is_finished_regtab"`
	RequestID   string `json:"request_id"`
	RegDateTime string `json:"reg_date"`
}

func getRegisteredTasks(db *sql.DB) ([]RegisteredTasksForClients, error) {
	var tasksList []RegisteredTasksForClients

	rows, err := db.Query(`
			SELECT
			id AS task_id,	
			request_id,
			COALESCE(expression, '') AS expression,
			COALESCE(result, 0) as result,
			COALESCE(status, '') AS status, 
			is_wrong,
			is_finished,
			COALESCE(comment, '') AS comment, 
			reg_date,
			COALESCE(TO_CHAR(finish_date, 'YYYY-MM-DD HH24:MI:SS'), 'default_value') AS finish_date,
			CASE 
				WHEN finish_date IS NOT NULL AND reg_date IS NOT NULL THEN
					CASE WHEN finish_date < reg_date THEN 0
					ELSE CAST(EXTRACT(EPOCH FROM (finish_date - reg_date)) AS INTEGER) END 
				ELSE 0
			END AS calc_duration
		FROM
			reg_expr
		ORDER BY reg_date DESC    
		LIMIT 100
	`)

	if err != nil {
		return nil, fmt.Errorf("error executing database query: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var task RegisteredTasksForClients
		if err := rows.Scan(
			&task.TaskID,
			&task.RequestID,
			&task.Expression,
			&task.Result,
			&task.Status,
			&task.IsWrong,
			&task.IsFinished,
			&task.Comment,
			&task.RegDateTime,
			&task.FinishDateTime,
			&task.CalcDuration); err != nil {
			// Логируем ошибку, но не прекращаем выполнение цикла
			log.Printf("Error scanning row: %v", err)
			continue
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		// Возвращаем ошибку вместе с пустым списком задач
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	return tasksList, nil
}

func processGet(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {

	registeredTasks, err := getRegisteredTasks(db)
	if err != nil {
		log.Printf("Error getting list of registered tasks (getRegisteredTasks): %v", err)
		http.Error(w, "Error getting list of registered tasks", http.StatusInternalServerError)
		return
	}

	responseJSON, err := json.Marshal(registeredTasks)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		log.Printf("Error encoding JSON: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)

}

func processingIncomingRequest(w http.ResponseWriter, r *http.Request, config Config, db *sql.DB) {
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

// /
// /
// /
// /
func getOverdueTasks(db *sql.DB, config Config) ([]TasksStruct, error) {
	var tasksList []TasksStruct

	rows, err := db.Query(`
		SELECT
			id,	
			task_id
		FROM
			calc_expr
		WHERE 
			is_finished = FALSE
			AND create_date < NOW() - ($1 || ' seconds')::interval
	`, config.MaxDurationForTask)

	if err != nil {
		return nil, fmt.Errorf("error executing database query: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var task TasksStruct
		if err := rows.Scan(
			&task.RecordID,
			&task.TaskID); err != nil {
			// Логируем ошибку, но не прекращаем выполнение цикла
			log.Printf("Error scanning row: %v", err)
			continue
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		// Возвращаем ошибку вместе с пустым списком задач
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	return tasksList, nil
}

func setCloseToOverdueTasks(db *sql.DB, tasks []TasksStruct) {
	for _, task := range tasks {
		// Завершаем задачу
		newValues := map[string]interface{}{
			"is_finished": true,
			"is_wrong":    true,
			"condition":   4,
			"comment":     "The allowed calculation timeout has been exceeded",
			"finish_date": time.Now(),
		}
		err := updateTaskAttributes(db, task.RecordID, newValues)
		if err != nil {
			log.Printf("Error save changes in incorrect task_id %d: %v", task.TaskID, err)
		}
		continue
	}
}

func closingOverdueTasks(db *sql.DB, config Config) error {

	if config.MaxDurationForTask > 0 {

		// Получаем суперстарые задачи для завершения
		overdueTasks, err := getOverdueTasks(db, config)
		if err != nil {
			return err
		}

		setCloseToOverdueTasks(db, overdueTasks)
	}

	return nil
}

// //
// //
// /
func getTasksForFinalisation(db *sql.DB) ([]TasksStruct, error) {
	var tasksList []TasksStruct

	rows, err := db.Query(`
		SELECT
			ce.id,	
			ce.task_id,
			CASE 
                WHEN ce.finish_date IS NOT NULL AND ce.create_date IS NOT NULL THEN
                    CASE WHEN ce.finish_date < ce.create_date THEN 0
                    ELSE CAST(EXTRACT(EPOCH FROM (ce.finish_date - ce.create_date)) AS INTEGER) END 
                ELSE 0
            END AS calc_duration,
			COALESCE(ce.result, '0') as result,
			ce.comment,
			ce.condition,
			ce.is_wrong,
			re.is_finished as is_finished_regtab
		FROM
			calc_expr AS ce
		LEFT JOIN reg_expr AS re ON ce.task_id = re.id
		WHERE ce.is_finished = TRUE
	`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var task TasksStruct
		err := rows.Scan(
			&task.RecordID,
			&task.TaskID,
			&task.CalcDuration,
			&task.Result,
			&task.Comment,
			&task.Condition,
			&task.IsWrong,
			&task.IsFinishedInRegTab)
		if err != nil {
			return nil, err
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasksList, nil
}

func setFinalStatusFinishedTasks(db *sql.DB, tasks []TasksStruct) {

	for _, task := range tasks {

		// Начало транзакции
		tx, err := db.Begin()
		if err != nil {
			log.Printf("Error starting transaction for task_id %d: %v", task.TaskID, err)
			return
		}
		defer func() {
			if err := recover(); err != nil {
				// Ошибка произошла, откатываем транзакцию
				log.Println("Rolling back transaction due to error:", err)
				tx.Rollback()
			}
		}()

		// Обработка задачи
		if task.IsFinishedInRegTab == nil {
			// В reg_expr задача отсутствует, удаляем запись из calc_expr
			// (ситуация маловероята, но возможна, т.к. не используется механизм внешних ключей)
			_, err := tx.Exec("DELETE FROM calc_expr WHERE id = $1", task.RecordID)
			if err != nil {
				panic(fmt.Sprintf("Error deleting calc_expr record for task_id %d: %v", task.TaskID, err))
			}
		} else if *task.IsFinishedInRegTab {
			// В reg_expr задача завершена, удаляем запись из calс_expr
			_, err := tx.Exec("DELETE FROM calc_expr WHERE id = $1", task.RecordID)
			if err != nil {
				panic(fmt.Sprintf("Error deleting calc_expr record for task_id %d: %v", task.TaskID, err))
			}

		} else {
			// В reg_expr задача не завершена, но завершена в calс_expr

			status := "success"
			if task.IsWrong {
				status = "error"
			} else {
				switch task.Condition {
				case 0:
					status = "new"
				case 1, 2:
					status = "success"
				case 3:
					status = "error"
				case 4:
					status = "cancel"
				}
			}

			_, err := tx.Exec(`
				UPDATE reg_expr
				SET
					is_finished = true,
					is_wrong = $1,
					result = $2,
					status = $3,
					comment = $4,
					calc_duration = $5,
					finish_date = $6
				WHERE
					id = $7
			`,
				task.IsWrong,
				task.Result,
				status,
				task.Comment,
				task.CalcDuration,
				time.Now(),
				task.TaskID)

			if err != nil {
				panic(fmt.Sprintf("Error finalizing reg_expr record for task_id %d: %v", task.TaskID, err))
			}

			// Удаляем запись из calс_expr
			_, err = tx.Exec("DELETE FROM calc_expr WHERE id = $1", task.RecordID)
			if err != nil {
				panic(fmt.Sprintf("Error deleting calc_expr record for task_id %d: %v", task.TaskID, err))
			}
		}

		// Фиксируем транзакцию
		if err := tx.Commit(); err != nil {
			log.Printf("Error committing transaction for task_id %d: %v", task.TaskID, err)
			return
		}
	}
}

func finalisationFinishedTasks(db *sql.DB) error {

	// Получаем задачи для финализации
	currentTasks, err := getTasksForFinalisation(db)
	if err != nil {
		return err
	}

	if len(currentTasks) > 0 {
		// Финализируем задачи
		setFinalStatusFinishedTasks(db, currentTasks)
	}

	return nil
}

// /
// /
// /
func getCurrentQueueLength(db *sql.DB) (int, error) {
	var queueLength int
	err := db.QueryRow("SELECT COUNT(*) FROM calc_expr").Scan(&queueLength)
	if err != nil {
		return 0, err
	}
	return queueLength, nil
}

func addNewTasksInQueue(db *sql.DB, count int) error {
	query := `
        INSERT INTO calc_expr (task_id, create_date, expression)
        SELECT
			re.id AS task_id,
			NOW() AS create_date,
			re.expression
        FROM
            reg_expr AS re
        LEFT JOIN
            calc_expr AS ce ON re.id = ce.task_id
        WHERE
			re.is_finished = FALSE
            AND ce.id IS NULL
		ORDER BY re.reg_date ASC 	
        LIMIT $1;`

	_, err := db.Exec(query, count)
	if err != nil {
		return err
	}

	return nil
}

func addNewTasksForCalculation(db *sql.DB, config Config) error {

	// Получаем текущую длину очереди задач
	queueLength, err := getCurrentQueueLength(db)
	if err != nil {
		log.Printf("Error getting current queue length: %v", err)
		return err // Выходим с ошибкой (продолжать не безопасно)
	}

	// Проверяем размер очереди
	MaxLenghtManagerQueue := config.MaxLenghtManagerQueue
	countNewTasks := MaxLenghtManagerQueue - queueLength
	if countNewTasks <= 0 {
		// Очередь заполнена
		return nil
	}

	// Добавляем новые задачи в очередь
	if err := addNewTasksInQueue(db, countNewTasks); err != nil {
		log.Printf("Error in query getting and adding new tasks to queue(addNewTasksInQueue): %v", err)
		return err
	}

	return nil
}

func makeTasksQueue(db *sql.DB, config Config) {

	// Завершим старые задачи
	if err := closingOverdueTasks(db, config); err != nil {
		log.Printf("Error occurred when closing overdue tasks(closingOverdueTasks): %v", err)
		return
	}

	// Финализируем завершенные задачи
	if err := finalisationFinishedTasks(db); err != nil {
		log.Printf("Error occurred when finalizing tasks(finalisationFinishedTasks): %v", err)
		return
	}

	// Добавляем в очередь новые задачи
	if err := addNewTasksForCalculation(db, config); err != nil {
		log.Printf("Error adding new tasks in queue(addNewTasksForCalculation): %v", err)
		return
	}

}

///
///
///

func getConditionZeroTasks(db *sql.DB) ([]TasksStruct, error) {
	var tasksList []TasksStruct

	rows, err := db.Query(`
		SELECT 
			id,
			task_id,
			expression
		FROM calc_expr
		WHERE 
			is_finished=FALSE  
			AND condition=0 
		ORDER BY create_date ASC
	`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var task TasksStruct
		err := rows.Scan(&task.RecordID, &task.TaskID, &task.Expression)
		if err != nil {
			return nil, err
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasksList, nil
}

// Обновляем значения атрибутов задачи в очереди(таблица calc_expr)
func updateTaskAttributes(db *sql.DB, RecordID int, newValues map[string]interface{}) error {
	query := "UPDATE calc_expr SET "
	params := make([]interface{}, 0)

	// Формируем SET часть запроса
	setValues := make([]string, 0)
	for key, value := range newValues {
		setValues = append(setValues, fmt.Sprintf("%s = $%d", key, len(params)+1))
		params = append(params, value)
	}
	query += strings.Join(setValues, ", ")

	// Добавляем условие для конкретной задачи
	query += fmt.Sprintf(" WHERE id = $%d", len(params)+1)
	params = append(params, RecordID)

	// Выполняем SQL-запрос
	_, err := db.Exec(query, params...)
	if err != nil {
		log.Printf("Error updating task attributes for task whith (Record)id %d: %v", RecordID, err)
		return err
	}

	return nil
}

func newTasksPreprocessing(db *sql.DB, config Config) {

	// Получаем задачи (condition=0)
	tasks, err := getConditionZeroTasks(db)
	if err != nil {
		log.Printf("Error getting new tasks for pre-processing (getConditionZeroTasks): %v", err)
		return
	}

	if len(tasks) == 0 {
		return
	}

	for _, task := range tasks {

		currExpression := strings.TrimSpace(task.Expression)
		if currExpression == "" {
			// Завершаем задачу, как не прошедшую валидацию
			newValues := map[string]interface{}{
				"is_finished": true,
				"is_wrong":    true,
				"condition":   3,
				"comment":     "An error in your expression(is empty)",
				"finish_date": time.Now(),
			}
			err = updateTaskAttributes(db, task.RecordID, newValues)
			if err != nil {
				log.Printf("Error save changes in incorrect task_id %d: %v", task.TaskID, err)
			}
			continue
		}

		postfixStr := ""
		if config.MakeRPNInManager {

			postfix, err := expression_processing.Parse(currExpression)
			if err != nil {
				log.Printf("Error getting RPN for task_id %d: %v", task.TaskID, err)

				// Завершаем задачу, как не прошедшую валидацию
				newValues := map[string]interface{}{
					"is_finished": true,
					"is_wrong":    true,
					"condition":   3,
					"comment":     "An error in your expression(getting RPN)",
					"finish_date": time.Now(),
				}
				err = updateTaskAttributes(db, task.RecordID, newValues)
				if err != nil {
					log.Printf("Error save changes in incorrect task_id %d: %v", task.TaskID, err)
				}
				continue
			}

			postfixStr = expression_processing.PostfixToString(postfix)
		}

		// Фиксируем задачу, как готовую к обработке
		newValues := map[string]interface{}{
			"rpn_expression": postfixStr,
			"is_wrong":       false,
			"condition":      1,
		}
		err = updateTaskAttributes(db, task.RecordID, newValues)
		if err != nil {
			log.Printf("Error save changes in new task_id %d: %v", task.TaskID, err)
		}

	}
}

// /
// /
// /
// /
func getConditionOneTasks(db *sql.DB) ([]TasksStruct, error) {
	var tasksList []TasksStruct

	rows, err := db.Query(`
		SELECT 
			id,
			task_id,
			expression,
			rpn_expression
		FROM calc_expr
		WHERE 
			is_finished=FALSE  
			AND condition=1 
		ORDER BY create_date ASC
	`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var task TasksStruct
		err := rows.Scan(&task.RecordID, &task.TaskID, &task.Expression, &task.RPNexpression)
		if err != nil {
			return nil, err
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasksList, nil
}

func sendTaskToCalcDaemon(task TasksStruct, config Config) error {

	data := map[string]interface{}{
		"task_id":        task.TaskID,
		"expression":     task.Expression,
		"rpn_expression": task.RPNexpression,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("error marshalling JSON data: %v", err)
	}

	ManagerEndpoint := config.DaemonServerAddress + config.DaemonEndpoint
	resp, err := http.Post(ManagerEndpoint, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error sending POST request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func processTaskAndSend(task TasksStruct, db *sql.DB, config Config) error {

	err := sendTaskToCalcDaemon(task, config)
	if err != nil {
		return fmt.Errorf("error sending task to calc daemon: %v", err)
	}

	// Установка condition=2 после обработки
	newValues := map[string]interface{}{
		"condition": 2,
	}
	if err := updateTaskAttributes(db, task.RecordID, newValues); err != nil {
		log.Printf("Error updating task attributes after processing for task_id %d: %v", task.TaskID, err)
		// Возвращаем ошибку, если произошла проблема при обновлении атрибутов задачи
		return fmt.Errorf("error updating task attributes: %v", err)
	}

	return nil
}

func worker(id int, tasksCh <-chan TasksStruct, doneCh chan<- bool, db *sql.DB, config Config) {
	for task := range tasksCh {
		if err := processTaskAndSend(task, db, config); err != nil {
			log.Printf("Error processing task: %v", err)
		}
	}

	// Горутина завершила работу
	doneCh <- true
}

func distributionOfTasks(db *sql.DB, config Config) {

	// Создаем каналы
	tasksCh := make(chan TasksStruct, config.MaxManagersWorkers)
	doneCh := make(chan bool)

	// Запускаем горутины-воркеры
	for i := 0; i < config.MaxManagersWorkers; i++ {
		go worker(i, tasksCh, doneCh, db, config)
	}

	// Получаем задачи для распределения(condition=1)
	tasks, err := getConditionOneTasks(db)
	if err != nil {
		log.Printf("Error getting dont distributed  (getConditionOneTasks): %v", err)
		return
	}

	// Отправляем задачи в канал
	for i := range tasks {
		tasksCh <- tasks[i]
	}

	// Закрываем канал задач, чтобы завершить работу горутин-воркеров
	close(tasksCh)

	// Ожидаем завершения всех горутин-воркеров
	for i := 0; i < config.MaxManagersWorkers; i++ {
		<-doneCh
	}
}

func processingTaskQueue(db *sql.DB, config Config) {

	newTasksPreprocessing(db, config)

	distributionOfTasks(db, config)
}

func runManagerLoop(db *sql.DB, config Config) {

	for {
		// Формирование очереди задач
		makeTasksQueue(db, config)

		// Обработка очереди задач
		processingTaskQueue(db, config)

		// Ожидание перед следующей итерацией
		if config.MainLoopDelaySeconds > 0 {
			time.Sleep(time.Duration(config.MainLoopDelaySeconds) * time.Second)
		}

	}
}

// /
// /
// /
func getConditionTwoTasks(db *sql.DB) ([]TasksStruct, error) {
	var tasksList []TasksStruct

	rows, err := db.Query(`
		SELECT 
			id,
			task_id,
			expression,
			rpn_expression
		FROM calc_expr
		WHERE 
			is_finished=FALSE  
			AND condition=2 
		ORDER BY create_date ASC
	`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var task TasksStruct
		err := rows.Scan(&task.RecordID, &task.TaskID, &task.Expression, &task.RPNexpression)
		if err != nil {
			return nil, err
		}
		tasksList = append(tasksList, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasksList, nil
}

func retrySendingCondition2(db *sql.DB, config Config) {

	tasks, err := getConditionTwoTasks(db)
	if err != nil {
		log.Printf("Error getting condition=2 tasks (getConditionTwoTasks): %v", err)
	}

	DaemonEndpoint := config.DaemonServerAddress + config.DaemonEndpoint

	for _, task := range tasks {
		// url := fmt.Sprintf("%s%s", DaemonEndpoint, task.TaskID)
		taskIDAsString := fmt.Sprintf("%d", task.TaskID)
		url := DaemonEndpoint + "?task_id=" + taskIDAsString

		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Error making GET request to daemon: %v", err)
			// Продолжаем со следующей задачей
			continue
		}
		// defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			// Код ответа не 200, повторяем отправку
			err := sendTaskToCalcDaemon(task, config)
			if err != nil {
				log.Printf("Error sending task to calc daemon: %v", err)
			}
		}
	}
}

func addingManagerLoop(db *sql.DB, config Config) {

	for {
		// Отправка возможно зависших заданий(condition=2)
		retrySendingCondition2(db, config)

		// Ожидание перед следующей итерацией
		if config.AddnigLoopDelaySeconds > 0 {
			time.Sleep(time.Duration(config.AddnigLoopDelaySeconds) * time.Second)
		}

	}
}

func main() {

	// Загрузка настроек приложения
	config, err := loadConfig("manager_config.json")
	if err != nil {
		log.Println("Error loading config (manager_config.json):", err)
		return
	}

	// Инициализация соединения с базой данных
	db, err := createDBConnection(config)
	if err != nil {
		// log.Println("Error creating DB connection:", err)
		log.Fatal("Error creating DB connection:", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Println("Error closing DB connection:", err)
		}
	}()

	// Инициализация HTTP сервера.
	http.HandleFunc(config.ManagerEndpoint, func(w http.ResponseWriter, r *http.Request) {
		// Добавляем заголовки CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

		// Проверяем метод запроса, и если это OPTIONS, завершаем обработку
		if r.Method == "OPTIONS" {
			return
		}

		// Хендлер-обработчик входящих запросов
		processingIncomingRequest(w, r, config, db)
	})

	// Запускаем HTTP сервер в горутине
	go func() {
		log.Println("Server 'manager' is listening on", config.ServerAddress)
		// log.Fatal(http.ListenAndServe(config.ServerAddress, nil))
		log.Println(http.ListenAndServe(config.ServerAddress, nil))
	}()

	// Запуск основного цикла в горутине(вычисление выражений)
	go runManagerLoop(db, config)

	go addingManagerLoop(db, config)

	// Бесконечный цикл для того, чтобы не дать программе завершиться
	select {}

}

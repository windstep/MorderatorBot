package main

import (
	"bot/internal/config"
	"bufio"
	"context"
	"encoding/json"
	"github.com/SevereCloud/vksdk/v2/api"
	"github.com/SevereCloud/vksdk/v2/events"
	"github.com/SevereCloud/vksdk/v2/longpoll-bot"
	"github.com/ostafen/clover"
	log "github.com/sirupsen/logrus"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var cfg *config.Config
var vk *api.VK
var db *clover.DB
var blacklistWords []string

const COLLECTION_NAME string = "chats"

type User struct {
	ID   int       `json:"id"`
	From time.Time `json:"from"`
}

type Chat struct {
	ChatId int    `json:"chat_id"`
	Users  []User `json:"users"`
}

func init() {
	log.SetFormatter(&log.TextFormatter{
		ForceColors:     true,
		ForceQuote:      true,
		FullTimestamp:   true,
		TimestampFormat: time.RFC822,
		PadLevelText:    true,
	})
}

func main() {
	log.Info("Создаю приложение")

	log.Info("Читаю конфигурационные файлы")
	cfg = config.Init()
	log.Info("Конфигурация прочитана успешно")

	log.Info("Подключаюсь к VK API")
	vk = api.NewVK(cfg.VkBotToken)
	lp, err := longpoll.NewLongPoll(vk, cfg.VkGroupId)
	if err != nil {
		log.Fatalf("Не получилось подключиться к long-poll сообщениям вконтакте: %+v", err)
	}
	log.Info("Подключение к VK API прошло успешно")

	lp.Wait = 5
	// Обработчик полученного сообщения - он будет вызван, когда мы получим сообщение от пользователя вк.
	lp.MessageNew(checkMessage)

	// Работаем с хранилищем. Инициализируем.
	log.Infof("Работаю с файлом хранилища: %s", cfg.DbFile)
	db, err = clover.Open(cfg.DbFile)
	if err != nil {
		log.Fatalf("Не удалось инициализировать базу данных: %+v", err)
	}

	collectionExists, err := db.HasCollection(COLLECTION_NAME)
	if err != nil {
		log.Fatalf("Не получается проверить существование коллекции: %+v", err)
	}

	if collectionExists == false {
		err = db.CreateCollection(COLLECTION_NAME)
		if err != nil {
			log.Fatalf("Не получается создать базу данных: %+v", err)
		}
	}

	log.Printf("Сканируем файлы на предмет слов для блокировки")
	blacklistWords = loadBlacklistWords(cfg.BlocklistFilename)
	log.Printf("Успешно распарсили файлы с блокируемыми словами ")

	if err := lp.Run(); err != nil {
		log.Fatalf("Не удалось запустить процесс загрузки событий с сервера: %+v", err)
	}
	defer lp.Shutdown()
}

func checkMessage(ctx context.Context, msg events.MessageNewObject) {
	log.Printf("Новое сообщение в чате %d", msg.Message.PeerID)

	// Эта функция делает слишком много. Надо немного изменить логику, чтобы упростить ее понимание.
	deleteMessageIfUserIsInBlacklist(msg.Message.FromID, msg.Message.PeerID, msg.Message.ConversationMessageID)

	// Затем в распарсим сообщение и удалим его, если в нем есть заблокированные слова, а также забаним пользователя.
	parseMessage(ctx, msg)
}

func deleteMessageIfUserIsInBlacklist(userId int, chatId int, messageId int) {
	c := getChatFromDB(chatId)

	for _, user := range c.Users {
		// Если наш пользователь находится в списке и время блокировки еще не прошло - удалим его сообщение
		if user.ID == userId && time.Now().Sub(user.From) < time.Hour*8 {
			log.Infof("Пользователь заблокирован! Надо удалить сообщение из чата.")
			deleteMessage(messageId, chatId)
		}

		// Если наш пользователь в списке, но время блокировки уже прошло - уберем его из этого списка
		if user.ID == userId && time.Now().Sub(user.From) > time.Hour*8 {
			log.Infof("Пользователь заблокирован, но уже давно. Уберем его из списка заблокированных, чтобы не мешался")
			removeUserFromBlocklist(userId, chatId)
		}
	}
}

func removeUserFromBlocklist(userId int, chatId int) {
	log.Printf("Пытаюсь разблокировать пользователя %d в чате %d", userId, chatId)
	doc := getChatFromDB(chatId)

	for i, user := range doc.Users {
		if user.ID == userId {
			doc.Users = append(doc.Users[:i], doc.Users[i+1:]...)
			break
		}
	}

	saveChat(doc, chatId)
}

func blockUser(userId, chatId int) {
	log.Infof("Блокирую пользователя %d в чате %d", userId, chatId)
	// Сначала получим то, что находится в списке заблокированных в чате
	// Для начала проверим, есть ли вообще чат в базе данных (ведь его может не быть, верно?)
	doc := getChatFromDB(chatId)

	// Проверим, присутствует ли наш пользователь в этом списке
	// Если присутствует - просто выйдем из функции
	for _, user := range doc.Users {
		if user.ID == userId {
			log.Errorf("Пользователь %d уже заблокирован в чате %d!", userId, chatId)
			return
		}
	}

	// Если не присутствует, то добавим его в список
	doc.Users = append(doc.Users, User{
		ID:   userId,
		From: time.Now(),
	})

	saveChat(doc, chatId)
}

func saveChat(doc Chat, chatId int) {
	log.Infof("Сохраняю обновления настроек блокировки для чата %d", chatId)
	// Сделаем из этого документ
	bts, err := json.Marshal(doc)
	if err != nil {
		log.Fatalf("Не получилось превратить документ в массив байт: %+v", err)
	}

	var updates map[string]interface{}
	err = json.Unmarshal(bts, &updates)
	if err != nil {
		log.Fatalf("Не получилось превратить массив байт в карту ключ-значение для записи в базу: %+v", err)
	}

	// Сохраним
	err = db.Query(COLLECTION_NAME).Where(clover.Field("chat_id").Eq(doc.ChatId)).Update(updates)
	if err != nil {
		log.Fatalf("Не удалось обновить запись в базе данных: %+v", err)
	}
}

func getChatFromDB(chatId int) Chat {
	docs, err := db.Query(COLLECTION_NAME).Where(clover.Field("chat_id").Eq(chatId)).FindAll()
	if err != nil {
		log.Fatalf("Произошла ошибка запроса данных из базы: %+v", err)
	}

	// Если у нас такого чата не существует - давайте его создадим
	// По результатам if-else у нас должен появиться документ
	doc := Chat{}
	var result *clover.Document
	if len(docs) == 0 {
		dbDoc := clover.NewDocumentOf(map[string]interface{}{
			"chat_id": chatId,
			"users":   []User{},
		})

		dbID, err := db.InsertOne(COLLECTION_NAME, dbDoc)
		if err != nil {
			log.Fatalf("Не удалось создать базу данных для чата: %+v", err)
		}

		result, err = db.Query(COLLECTION_NAME).FindById(dbID)
		if err != nil {
			log.Fatalf("Не удалось запросить данные из базы по ID: %s, ошибка: %+v", dbID, err)
		}
	} else {
		result = docs[0]
	}

	err = result.Unmarshal(&doc)
	if err != nil {
		log.Fatalf("Не удалось распарсить ответ базы данных в структуру: %+v", err)
	}

	return doc
}

func parseMessage(ctx context.Context, msg events.MessageNewObject) {
	// Для парсинга сообщения я предлагаю сформировать regexp, чтобы оно работало быстрее
	// И именно regexp=структуры можно было использовать в списке блокируемых слов.
	log.Infof("Обрабатываю сообщение %d в чате %d", msg.Message.ConversationMessageID, msg.Message.PeerID)

	reg := getRegexp()

	// Проверим наше сообщение на предмет запрещенных слов
	matched := reg.MatchString(msg.Message.Text)
	if matched {
		// Здесь мы уже уверены, что сообщение содержит запрещенные слова - заблокируем пользователя и удалим сообщение
		blockUser(msg.Message.FromID, msg.Message.PeerID)
		deleteMessage(msg.Message.ConversationMessageID, msg.Message.PeerID)
	}
}

// Формируем регулярное выражение из списка тех слов, что были распаршены из текста.
// Главный показатель: слово должно быть представлено целиком, разделенным пробелами, или знаками препинания.
// todo: Сделать выполнение этой функции однократным, чтобы минимизировать накладные расходы на его формирование.
func getRegexp() *regexp.Regexp {
	regexpBlacklistedWords := strings.Join(blacklistWords, "|")
	regexpFull := strings.Join([]string{
		`(?i)(^|\s)(`,
		regexpBlacklistedWords,
		`)($|\s|\,|\.|\!|\?)`,
	}, "")

	// Используем Compile вместо MustCompile для того, чтобы кастомизировать сообщение об ошибке.
	// Де-факто, это все равно, что вызывать MustCompile.
	reg, err := regexp.Compile(regexpFull)
	if err != nil {
		log.Fatalf("Не могу сформировать регулярное выражение: %+v", err)
	}

	return reg
}

// Функция загрузит в память приложения те слова, которые у нас внесены в список запрещенных.
func loadBlacklistWords(filename string) []string {
	log.Infof("Читаю слова для блокировки из файла %s", filename)
	if _, err := os.Stat(filename); err == os.ErrNotExist {
		log.Fatalf("Не удается прочитать файл с запрещенными словами")
	}

	file, err := os.Open(filename)
	if err != nil {
		log.Fatalf("Ошибка открытия файла: %+v", err)
	}

	defer file.Close()

	var output []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		output = append(output, scanner.Text())
	}

	return output
}

func deleteMessage(messageId, chatId int) {
	log.Infof("Удаляю сообщение %d в чате %d", messageId, chatId)
	_, err := vk.MessagesDelete(api.Params{
		"cmids":          strconv.Itoa(messageId),
		"spam":           0,
		"delete_for_all": 1,
		"peer_id":        chatId,
	})

	if err != nil {
		log.Errorf("Не могу удалить сообщение: %+v", err)
	}
}

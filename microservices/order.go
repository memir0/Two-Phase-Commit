package main

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strings"

	"../micro"
	_ "github.com/go-sql-driver/mysql"
)

const CONN_PORT = "3335"

var list micro.List
var host = ""

type Order struct {
	User_id int `json:"user_id"`
	Amount  int `json:"amount"`
}

func handlePrepare(conn net.Conn, password string) micro.Prep {
	// Read user id and amount of items
	buf := make([]byte, 4)
	_, err := conn.Read(buf)
	data := binary.BigEndian.Uint32(buf[:4])
	user_id := int(data)
	if err != nil {
		fmt.Println("Error reading:", err.Error())
		return micro.Prep{0, nil, 0} // Error reading data
	}
	_, err = conn.Read(buf)
	data = binary.BigEndian.Uint32(buf[:4])
	amount := int(data)
	if err != nil {
		fmt.Println("Error reading:", err.Error())
		return micro.Prep{0, nil, 0} // Error reading datas
	}

	items := make(map[int]int)

	// Read items
	for i := 0; i < amount; i++ {
		_, err = conn.Read(buf)
		if err != nil {
			fmt.Println("Error reading:", err.Error())
			return micro.Prep{0, nil, 0} // Error reading datas
		}
		data = binary.BigEndian.Uint32(buf[:4])
		item := int(data)
		items[item]++
	}

	fmt.Println(items)

	fmt.Println(user_id, amount)
	list.Mux.Lock()
	// Check if the user is busy
	if list.List[user_id] {
		fmt.Println("user_id already in list of prepared transactions")
		list.Mux.Unlock()
		return micro.Prep{3, nil, user_id}
	}
	list.List[user_id] = true
	list.Mux.Unlock()
	// Connect to DB
	db, err := sql.Open("mysql", password+"/order_service")
	if err != nil {
		return micro.Prep{4, nil, user_id}
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil { //7 = could not start transaction
		fmt.Println(err)
		return micro.Prep{5, tx, user_id}
	}

	for item, count := range items {
		// Check if stock of items will suffice for the order
		results, err := db.Query("SELECT amount FROM `items` WHERE item_id=?", item)
		var total_from_db int
		for results.Next() {
			err = results.Scan(&total_from_db)
			if err != nil {
				return micro.Prep{10, tx, user_id} //
			}
			fmt.Println(total_from_db)
			if total_from_db < count {
				return micro.Prep{12, tx, user_id}
			}
		}

		_, err = tx.Exec("UPDATE `items` SET amount = amount - 1  WHERE item_id=?", item)
		if err != nil {
			return micro.Prep{6, tx, user_id}
		}

	}

	_, err = tx.Exec("INSERT INTO `order` (order_id, user_id, amount) VALUES (DEFAULT, ?, ?)", user_id, amount)

	if err != nil {
		tx.Rollback() // 8 = Could not lock row
		return micro.Prep{6, tx, user_id}
	}

	return micro.Prep{1, tx, user_id}
}

func main() {
	// Hashmap of users to lock other threads out from modifying the same user
	list = micro.List{List: make(map[int]bool)}
	// Read database config
	data, err := ioutil.ReadFile("../.config")
	if err != nil {
		fmt.Println("File reading error", err)
		os.Exit(1)
	}
	password := strings.TrimSpace(string(data))

	data, err = ioutil.ReadFile("../addresses")
	if err != nil {
		fmt.Println("File reading error", err)
		os.Exit(1)
	}
	host = strings.Split(string(data), " ")[2]
	fmt.Println("HOST: ", host)
	// Listen for incomming requests
	socket, err := net.Listen(micro.CONN_TYPE, host+":"+CONN_PORT)
	if err != nil {
		fmt.Println("Error listening: ", err.Error())
		os.Exit(1)
	}
	defer socket.Close()
	fmt.Println("Listening on " + host + ":" + CONN_PORT)

	for {
		fmt.Println("started new connection")

		conn, err := socket.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err.Error())
			os.Exit(1)
		}
		go prepareAndCommit(conn, password)
	}
}

func prepareAndCommit(conn net.Conn, password string) {
	// Attempts to prepear transaction
	prep := handlePrepare(conn, password)
	tx := prep.Tx
	user_id := prep.User_id
	b := make([]byte, 2)
	fmt.Println(prep.Id)
	binary.LittleEndian.PutUint16(b, uint16(prep.Id))
	// Write status to coordinator
	conn.Write(b)
	// Read response from coordinator an handle it
	micro.HandleCommit(conn, tx, user_id, list, prep.Id)
}

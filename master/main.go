package main

import (
	"512SvMan/db"
	"512SvMan/env512"
	"512SvMan/nfs"
	"512SvMan/npm"
	"512SvMan/protocol"
	"fmt"
	"log"
	"os"

	logger "github.com/Maruqes/512SvMan/logger"
	"google.golang.org/grpc"
)

func newSlave(addr, machineName string, conn *grpc.ClientConn) error {
	err := nfs.MountAllSharedFolders(protocol.GetAllGRPCConnections(), protocol.GetAllMachineNames())
	if err != nil {
		return err
	}
	return nil
}

func askForSudo() {
	//if current program is not sudo terminate
	if os.Geteuid() != 0 {
		fmt.Println("This program needs to be run as root.")
		os.Exit(0)
	}
}

func main() {
	askForSudo()

	if err := env512.Setup(); err != nil {
		log.Fatalf("env setup: %v", err)
	}
	logger.SetType(env512.Mode)

	db.InitDB()
	//create all tables if not exists
	err := db.CreateNFSTable()
	if err != nil {
		log.Fatalf("create NFS table: %v", err)
	}

	//listen and connects to gRPC
	protocol.ListenGRPC(newSlave)

	hostAdmin := "127.0.0.1:81"
	base := "http://" + hostAdmin

	token, err := npm.SetupNPM(base)

	if err != nil {
		panic(err)
	}

	println("NPM setup complete, token:", token)

	webServer()

	// xml, err := virsh.CreateVMCustomCPU(
	// 	"qemu:///system",
	// 	"debian-kde",
	// 	8192, 6,
	// 	"/mnt/data/debian-live-13.1.0-amd64-kde.iso", 50, // relativo -> /var/512SvMan/qcow2/debian-kde.qcow2
	// 	"/mnt/data/debian.qcow2", // relativo -> /var/512SvMan/iso/...
	// 	"",                                 // machine (user decide; "" = auto)
	// 	"default", "0.0.0.0",
	// 	"Westmere", nil, // baseline portable
	// )
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// fmt.Println("XML gravado em:", xml)

	select {}
}

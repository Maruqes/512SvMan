package protocol

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"slave/env512"
	nfsservice "slave/nfs"
	"slave/virsh"
	"syscall"
	"time"

	nfsproto "github.com/Maruqes/512SvMan/api/proto/nfs"
	pb "github.com/Maruqes/512SvMan/api/proto/protocol"
	grpcVirsh "github.com/Maruqes/512SvMan/api/proto/virsh"

	"github.com/Maruqes/512SvMan/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	args := os.Args
	env := os.Environ()

	log.Println("Restarting process...")
	return syscall.Exec(exe, args, env)
}

// === Servidor do CLIENTE (ClientService) ===
type clientServer struct {
	pb.UnimplementedClientServiceServer
}

// serve para ser pingado e ver se esta vivo
func (s *clientServer) Notify(ctx context.Context, req *pb.NotifyRequest) (*pb.NotifyResponse, error) {
	return &pb.NotifyResponse{Ok: "OK do Cliente"}, nil
}

func listenGRPC() {
	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()

	//registar services
	pb.RegisterClientServiceServer(s, &clientServer{})
	nfsproto.RegisterNFSServiceServer(s, &nfsservice.NFSService{})
	grpcVirsh.RegisterSlaveVirshServiceServer(s, &virsh.SlaveVirshService{})
	log.Println("Cliente a ouvir em :50052")
	if err := s.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func monitorConnection(conn *grpc.ClientConn) {
	ctx := context.Background()
	for {
		state := conn.GetState()
		switch state {
		case connectivity.Ready:
			// healthy, nothing to do
		case connectivity.Idle:
			log.Printf("connection to master idle, forcing reconnect")
			conn.Connect()
		case connectivity.Connecting:
			log.Printf("connection to master reconnecting...")
		case connectivity.Shutdown, connectivity.TransientFailure:
			log.Printf("connection to master lost (state: %s), restarting", state)
			_ = conn.Close()
			if err := restartSelf(); err != nil {
				log.Printf("failed to restart slave process: %v", err)
			}
			os.Exit(1)
			return
		default:
			log.Printf("connection state changed: %s", state)
		}
		if !conn.WaitForStateChange(ctx, state) {
			log.Println("monitorConnection: no further state changes, stopping monitor")
			return
		}
	}
}

func PingMaster(conn *grpc.ClientConn) {
	for {
		h := pb.NewProtocolServiceClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := h.Notify(ctx, &pb.NotifyRequest{Text: "Ping do Slave"})
		cancel()
		if err != nil {
			logger.Error("PingMaster: %v", err)
		}
		//ping every 30 seconds
		time.Sleep(time.Duration(env512.PingInterval) * time.Second)
	}
}

func ConnectGRPC() *grpc.ClientConn {

	target := fmt.Sprintf("%s:50051", env512.MasterIP)
	go listenGRPC()

	for {
		log.Println("Connecting to master at", target)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		conn, err := grpc.DialContext(ctx, target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		cancel()
		if err != nil {
			log.Printf("dial master failed: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		h := pb.NewProtocolServiceClient(conn)
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 60*time.Second)
		outR, err := h.SetConnection(reqCtx, &pb.SetConnectionRequest{Addr: env512.SlaveIP, MachineName: env512.MachineName})
		reqCancel()
		if err != nil {
			log.Printf("SetConnection failed: %v", err)
			conn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("Resposta do master: %s", outR.GetOk())
		go monitorConnection(conn)
		go PingMaster(conn)
		return conn
	}
}

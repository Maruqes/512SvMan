package virsh

import (
	"context"
	"slave/info"

	grpcVirsh "github.com/Maruqes/512SvMan/api/proto/virsh"
)

type SlaveVirshService struct {
	grpcVirsh.UnimplementedSlaveVirshServiceServer
}

// por algum motivo o grpc quer context aqui ahah, so Deus sabe, so Deus faz
func (s *SlaveVirshService) GetCpuFeatures(ctx context.Context, e *grpcVirsh.Empty) (*grpcVirsh.GetCpuFeaturesResponse, error) {
	cpu, err := info.CPUInfo.GetCPUInfo()
	if err != nil {
		return nil, err
	}
	return &grpcVirsh.GetCpuFeaturesResponse{Features: cpu.FeatureSet()}, nil
}

func (s *SlaveVirshService) CreateVm(ctx context.Context, req *grpcVirsh.CreateVmRequest) (*grpcVirsh.OkResponse, error) {
	params := VMCreationParams{
		ConnURI:        "qemu:///system",
		Name:           req.Name,
		MemoryMB:       int(req.Memory),
		VCPUs:          int(req.Vcpu),
		DiskPath:       req.DiskPath,
		DiskSizeGB:     int(req.DiskSizeGB),
		ISOPath:        req.IsoPath,
		Network:        req.Network,
		GraphicsListen: "0.0.0.0",
		VNCPassword:    req.VNCPassword,
	}
	_, err := CreateVMHostPassthrough(params)
	if err != nil {
		return nil, err
	}
	return &grpcVirsh.OkResponse{Ok: true}, nil
}

func (s *SlaveVirshService) GetAllVms(ctx context.Context, e *grpcVirsh.Empty) (*grpcVirsh.GetAllVmsResponse, error) {
	vms, err := GetAllVMs()
	if err != nil {
		return nil, err
	}
	return &grpcVirsh.GetAllVmsResponse{Vms: vms}, nil
}

func (s *SlaveVirshService) GetVmByName(ctx context.Context, req *grpcVirsh.GetVmByNameRequest) (*grpcVirsh.Vm, error) {
	vm, err := GetVMByName(req.Name)
	if err != nil {
		return nil, err
	}
	return vm, nil
}

package services

import (
	"512SvMan/db"
	"512SvMan/nfs"
	"512SvMan/protocol"
	"fmt"
	"strings"

	proto "github.com/Maruqes/512SvMan/api/proto/nfs"
	"github.com/Maruqes/512SvMan/logger"
)

func getFolderName(path string) string {
	path = strings.TrimSuffix(path, "/")

	//split by /
	parts := []rune(path)
	name := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' {
			break
		}
		name = string(parts[i]) + name
	}
	return name
}

func ConvertNSFShareToGRPCFolderMount(share []db.NFSShare) *proto.FolderMountList {
	folderMounts := &proto.FolderMountList{
		Mounts: make([]*proto.FolderMount, 0, len(share)),
	}
	for _, s := range share {
		folderMounts.Mounts = append(folderMounts.Mounts, &proto.FolderMount{
			MachineName: s.MachineName,
			FolderPath:  s.FolderPath,
			Source:      s.Source,
			Target:      s.Target,
		})
	}
	return folderMounts
}

type SharePoint struct {
	MachineName string `json:"machine_name"` //this machine want to share
	FolderPath  string `json:"folder_path"`  //this folder
}

type NFSService struct {
	SharePoint SharePoint
}

func (s *NFSService) CreateSharePoint() error {
	//find connection by machine name
	conn := protocol.GetConnectionByMachineName(s.SharePoint.MachineName)
	if conn == nil || conn.Connection == nil {
		return fmt.Errorf("slave not connected")
	}

	mount := &proto.FolderMount{
		MachineName: s.SharePoint.MachineName,                  // machine that shares
		FolderPath:  s.SharePoint.FolderPath,                   // folder to share
		Source:      conn.Addr + ":" + s.SharePoint.FolderPath, // creates ip:folderpath
		Target:      "/mnt/512SvMan/shared/" + s.SharePoint.MachineName + "_" + getFolderName(s.SharePoint.FolderPath),
	}

	if err := nfs.CreateSharedFolder(conn.Connection, mount); err != nil {
		logger.Error("CreateSharedFolder failed: %v", err)
		return err
	}

	err := db.AddNFSShare(mount.MachineName, mount.FolderPath, mount.Source, mount.Target)
	if err != nil {
		logger.Error("AddNFSShare failed: %v", err)
		return err
	}

	err = s.SyncSharedFolder()
	if err != nil {
		logger.Error("SyncSharedFolder failed: %v", err)
		return err
	}

	err = s.MountAllSharedFolders()
	if err != nil {
		logger.Error("MountAllSharedFolders failed: %v", err)
		return err
	}
	return nil
}

func (s *NFSService) DeleteSharePoint() error {
	conn := protocol.GetConnectionByMachineName(s.SharePoint.MachineName)
	if conn == nil || conn.Connection == nil {
		return fmt.Errorf("slave not connected")
	}

	//check if exists in db
	if exists, err := db.DoesExistNFSShare(s.SharePoint.MachineName, s.SharePoint.FolderPath); err != nil {
		return fmt.Errorf("failed to check if NFS share exists: %v", err)
	} else if !exists {
		return fmt.Errorf("NFS share does not exist")
	}

	//remove last slash
	mount := &proto.FolderMount{
		MachineName: s.SharePoint.MachineName,
		FolderPath:  s.SharePoint.FolderPath,
		Source:      conn.Addr + ":" + s.SharePoint.FolderPath,
		Target:      "/mnt/512SvMan/shared/" + s.SharePoint.MachineName + "_" + getFolderName(s.SharePoint.FolderPath),
	}

	if err := nfs.RemoveSharedFolder(conn.Connection, mount); err != nil {
		return fmt.Errorf("failed to remove shared folder: %v", err)
	}

	err := db.RemoveNFSShare(mount.MachineName, mount.FolderPath)
	if err != nil {
		return fmt.Errorf("failed to remove NFS share from database: %v", err)
	}

	return nil
}

func (s *NFSService) GetAllSharedFolders() ([]db.NFSShare, error) {
	return nfs.GetAllSharedFolders()
}

// get all shared folders for each slave and make sure they are shared
func (s *NFSService) SyncSharedFolder() error {
	slavesShared, err := db.GetAllMachineNamesWithShares()
	if err != nil {
		return fmt.Errorf("failed to get all machine names with shares: %v", err)
	}

	notConnected := []string{}
	for _, machineName := range slavesShared {
		conn := protocol.GetConnectionByMachineName(machineName)
		if conn == nil || conn.Connection == nil {
			logger.Warn("slave not connected:", machineName)
			notConnected = append(notConnected, machineName)
			continue
		}

		shares, err := db.GetNFSharesByMachineName(machineName)
		if err != nil {
			logger.Error("failed to get NFS shares by machine name:", err)
			continue
		}

		nfs.SyncSharedFolder(conn.Connection, ConvertNSFShareToGRPCFolderMount(shares))
	}

	if len(notConnected) > 0 {
		return fmt.Errorf("some slaves not connected: %v", notConnected)
	}

	return nil
}

func (s *NFSService) MountAllSharedFolders() error {
	conns := protocol.GetAllGRPCConnections()
	machineNames := protocol.GetAllMachineNames()

	serversNFS, err := nfs.GetAllSharedFolders()
	if err != nil {
		return err
	}

	if len(conns) != len(machineNames) {
		return fmt.Errorf("length of connections and machine names must be the same")
	}

	logger.Info("Creating NFS shared folders on all slaves...")
	// create shared folders on all provided connections
	for _, svNSF := range serversNFS {
		mount := &proto.FolderMount{
			FolderPath:  svNSF.FolderPath,
			Source:      svNSF.Source,
			Target:      svNSF.Target,
			MachineName: svNSF.MachineName,
		}
		for i, conn := range conns {
			if conn == nil {
				continue
			}

			// skip if machine name does not match
			if machineNames[i] != svNSF.MachineName {
				continue
			}
			logger.Info("Creating NFS shared folder on machine:", machineNames[i], " with mount:", mount)
			// create shared folder on the specific machine
			if err := nfs.CreateSharedFolder(conn, mount); err != nil {
				return err
			}
		}
	}

	logger.Info("Mounting NFS shared folders on all slaves...")
	// mount on all provided connections
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		for _, svNSF := range serversNFS {
			mount := &proto.FolderMount{
				FolderPath:  svNSF.FolderPath,
				Source:      svNSF.Source,
				Target:      svNSF.Target,
				MachineName: svNSF.MachineName,
			}
			logger.Info("Mounting NFS shared folder on machine with mount:", mount)
			if err := nfs.MountSharedFolder(conn, mount); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *NFSService) UpdateNFSShit() error {
	err := s.SyncSharedFolder()
	if err != nil {
		logger.Error("SyncSharedFolder failed: %v", err)
		return err
	}

	err = s.MountAllSharedFolders()
	if err != nil {
		return err
	}
	return nil
}

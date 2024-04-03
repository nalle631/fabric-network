/*
Copyright 2021 IBM All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	mspID        = "Org1MSP"
	cryptoPath   = "../../test-network/organizations/peerOrganizations/org1.example.com"
	certPath     = cryptoPath + "/users/User1@org1.example.com/msp/signcerts/User1@org1.example.com-cert.pem"
	keyPath      = cryptoPath + "/users/User1@org1.example.com/msp/keystore/"
	tlsCertPath  = cryptoPath + "/peers/peer0.org1.example.com/tls/ca.crt"
	peerEndpoint = "localhost:7051"
	gatewayPeer  = "peer0.org1.example.com"
)

type CustomerParams struct {
	CustomerID string `json:"CustomerID"`
}

type CreateMowerParams struct {
	MowerID           string  `json:"MowerID"`
	ServiceLevel      string  `json:"ServiceLevel"`
	TargetGrassLength float32 `json:"TargetGrassLength"`
	MaxGrassLength    float32 `json:"MaxGrassLength"`
	MinGrassLength    float32 `json:"MinGrassLength"`
}

type UpdateServiceLevelParams struct {
	CustomerID   string `json:"CustomerID"`
	ServiceLevel string `json:"ServiceLevel"`
}

type UpdateTargetGrassLengthParams struct {
	CustomerID        string  `json:"CustomerID"`
	TargetGrassLength float32 `json:"TargetGrassLength"`
}

type UpdateGrassLengthIntervalParams struct {
	CustomerID     string  `json:"CustomerID"`
	MaxGrassLength float32 `json:"MaxGrassLength"`
	MinGrassLength float32 `json:"MinGrassLength"`
}

type RemoveMowerSLAParams struct {
	CustomerID string `json:"CustomerID"`
	MowerID    string `json:"MowerID"`
}

type Customer struct {
	ID   string `json:"ID"`
	SLAs []SLA  `json:"SLAs"`
}

type SlaParams struct {
	ServiceLevel      string  `json:"ServiceLevel"`
	TargetGrassLength float32 `json:"TargetGrassLength"`
	MaxGrassLength    float32 `json:"MaxGrassLength"`
	MinGrassLength    float32 `json:"MinGrassLength"`
}

type SLA struct {
	AppraisedValue int `json:"AppraisedValue,omitempty"`
	SlaParams
	ID string `json:"ID"`
}

func main() {
	router := CreateRouter()
	StartRouter(router)

}

// newGrpcConnection creates a gRPC connection to the Gateway server.
func newGrpcConnection() *grpc.ClientConn {
	certificate, err := loadCertificate(tlsCertPath)
	if err != nil {
		panic(err)
	}

	certPool := x509.NewCertPool()
	certPool.AddCert(certificate)
	transportCredentials := credentials.NewClientTLSFromCert(certPool, gatewayPeer)

	connection, err := grpc.Dial(peerEndpoint, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		panic(fmt.Errorf("failed to create gRPC connection: %w", err))
	}

	return connection
}

// newIdentity creates a client identity for this Gateway connection using an X.509 certificate.
func newIdentity() *identity.X509Identity {
	certificate, err := loadCertificate(certPath)
	if err != nil {
		panic(err)
	}

	id, err := identity.NewX509Identity(mspID, certificate)
	if err != nil {
		panic(err)
	}

	return id
}

func loadCertificate(filename string) (*x509.Certificate, error) {
	certificatePEM, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate file: %w", err)
	}
	return identity.CertificateFromPEM(certificatePEM)
}

// newSign creates a function that generates a digital signature from a message digest using a private key.
func newSign() identity.Sign {
	files, err := os.ReadDir(keyPath)
	if err != nil {
		panic(fmt.Errorf("failed to read private key directory: %w", err))
	}
	privateKeyPEM, err := os.ReadFile(path.Join(keyPath, files[0].Name()))

	if err != nil {
		panic(fmt.Errorf("failed to read private key file: %w", err))
	}

	privateKey, err := identity.PrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		panic(err)
	}

	sign, err := identity.NewPrivateKeySign(privateKey)
	if err != nil {
		panic(err)
	}

	return sign
}

func StartRouter(r *gin.Engine) {
	r.Run(":5001")
	// srv := &http.Server{
	// 	Addr:    ":8080", // Set port number
	// 	Handler: r,
	// }

	// err := srv.ListenAndServeTLS(certPath, keyPath)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// r.RunTLS(":8080", tlsCertPath, tlsKeyPath)
}

func CreateRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/contract/:id", ReadCustomerHandler)
	r.GET("/sla/:id", ReadSLAHandler)
	r.GET("/sla/:id/servicelevel", GetServiceLevelHandler)
	r.POST("/contract", CreateCustomerHandler)
	r.POST(":customer_id/sla", CreateMowerHandler)
	r.PUT("/sla/:id/grasslength", updateTargetGrassLengthHandler)
	r.PUT("/sla/:id/intervall", updateGrassLengthIntervalHandler)
	r.PUT("sla/:id/servicelevel", updateServiceLevelHandler)
	r.POST("/sla/evaluate", evaluateSLAHandler)
	r.DELETE("/sla/:id", removeMowerSLAHandler)
	return r
}

func createCustomer(contract *client.Contract, customerID string) {
	fmt.Printf("\n--> Submit Transaction: createCustomer, function creates a key value pair on the ledger \n")

	_, err := contract.SubmitTransaction("CreateCustomer", customerID)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Printf("*** Transaction committed successfully\n")
}

func CreateCustomerHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	var customerParams CustomerParams
	if err := c.BindJSON(&customerParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	createCustomer(contract, customerParams.CustomerID)
	c.IndentedJSON(http.StatusOK, gin.H{"message": "Customer created successfully"})
}

func createMower(contract *client.Contract, customerID string, mowerID string, serviceLevel string, targetgrasslength float32, maxgrasslength float32, mingrasslength float32) {
	fmt.Println("\n--> Submit Transaction: createMower")
	targetgrasslength_string := fmt.Sprintf("%f", targetgrasslength)
	maxgrasslength_string := fmt.Sprintf("%f", maxgrasslength)
	mingrasslength_string := fmt.Sprintf("%f", mingrasslength)

	_, err := contract.SubmitTransaction("CreateMower", customerID, mowerID, serviceLevel, targetgrasslength_string, maxgrasslength_string, mingrasslength_string)

	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Printf("*** Transaction committed successfully\n")
}

func CreateMowerHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	var mowerParams CreateMowerParams
	customerID := c.Param("customer_id")
	if err := c.BindJSON(&mowerParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	createMower(contract, customerID, mowerParams.MowerID, mowerParams.ServiceLevel, mowerParams.TargetGrassLength, mowerParams.MaxGrassLength, mowerParams.MinGrassLength)
	c.IndentedJSON(http.StatusOK, gin.H{"message": "Mower craeted successfully"})
}

func updateServiceLevel(contract *client.Contract, customerID string, mowerID string, serviceLevel string) error {
	fmt.Println("\n--> Submit Transaction: updateServiceLevel")

	_, err := contract.SubmitTransaction("UpdateServiceLevel", customerID, mowerID, serviceLevel)

	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			return fmt.Errorf("unexpected error type %T: %w", err, err)
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
		return err
	}
	return nil
}

func updateServiceLevelHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	slaID := c.Param("id")
	var updateServiceLevelParams UpdateServiceLevelParams
	if err := c.BindJSON(&updateServiceLevelParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err = updateServiceLevel(contract, updateServiceLevelParams.CustomerID, slaID, updateServiceLevelParams.ServiceLevel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.IndentedJSON(http.StatusOK, gin.H{"message": "Service level updated successfully"})
}

// Submit a transaction to query ledger state.
func updateTargetGrassLength(contract *client.Contract, customerID string, mowerID string, targetgrasslength float32) {
	fmt.Println("\n--> Submit Transaction: updateTargetGrassLength \n")
	fmt.Println(targetgrasslength)
	targetgrasslength_string := fmt.Sprintf("%f", targetgrasslength)
	fmt.Println(targetgrasslength_string)

	submitResult, err := contract.SubmitTransaction("UpdateTargetGrassLength", customerID, mowerID, targetgrasslength_string)

	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Println("Result:", submitResult)
}

func updateTargetGrassLengthHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	slaID := c.Param("id")
	var updateTargetGrassLengthParams UpdateTargetGrassLengthParams
	if err := c.BindJSON(&updateTargetGrassLengthParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updateTargetGrassLength(contract, updateTargetGrassLengthParams.CustomerID, slaID, updateTargetGrassLengthParams.TargetGrassLength)
	c.IndentedJSON(http.StatusOK, gin.H{"message": "TargetGrassLength updated successfully"})
}

func updateGrassLengthInterval(contract *client.Contract, customerID string, mowerID string, maxgrasslength float32, mingrasslength float32) {
	fmt.Println("\n--> Submit Transaction: updateGrassLengthInterval \n")

	maxgrasslength_string := fmt.Sprintf("%f", maxgrasslength)
	mingrasslength_string := fmt.Sprintf("%f", mingrasslength)
	submitResult, err := contract.SubmitTransaction("UpdateGrassLengthInterval", customerID, mowerID, maxgrasslength_string, mingrasslength_string)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Println("Result:", submitResult)
}

func updateGrassLengthIntervalHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	slaID := c.Param("id")
	var updateGrassLengthIntervalParams UpdateGrassLengthIntervalParams
	if err := c.BindJSON(&updateGrassLengthIntervalParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updateGrassLengthInterval(contract, updateGrassLengthIntervalParams.CustomerID, slaID, updateGrassLengthIntervalParams.MaxGrassLength, updateGrassLengthIntervalParams.MinGrassLength)
	c.IndentedJSON(http.StatusOK, gin.H{"message": "GrassLengthInterval updated successfully"})
}

func removeMowerSLA(contract *client.Contract, customerID string, mowerID string) {
	fmt.Println("\n--> Submit Transaction: updateGrassLengthInterval \n")

	submitResult, err := contract.SubmitTransaction("RemoveMowerSLA", customerID, mowerID)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Println("Result:", submitResult)
}

func removeMowerSLAHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	var removeMowerSLAParams RemoveMowerSLAParams
	if err := c.BindJSON(&removeMowerSLAParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	removeMowerSLA(contract, removeMowerSLAParams.CustomerID, removeMowerSLAParams.MowerID)
	c.IndentedJSON(http.StatusOK, gin.H{"message": "Mower SLA removed successfully"})
}

func evaluateSLA(contract *client.Contract, sla SlaParams) (int, error) {
	fmt.Printf("\n--> Evaluate Transaction: EvaluateSLA, function returns evaluation of an SLA\n")
	maxgrasslength_string := fmt.Sprintf("%f", sla.MaxGrassLength)
	mingrasslength_string := fmt.Sprintf("%f", sla.MinGrassLength)
	targetgrasslength_string := fmt.Sprintf("%f", sla.TargetGrassLength)
	evaluateResult, err := contract.EvaluateTransaction("EvaluateSLA", sla.ServiceLevel, targetgrasslength_string, maxgrasslength_string, mingrasslength_string)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
		return 0, err
	}
	evaluateResult_string := string(evaluateResult[:])

	return strconv.Atoi(evaluateResult_string)
}

func evaluateSLAHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "mower"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)
	contract := network.GetContract(chaincodeName)
	var slaParams SlaParams
	if err := c.BindJSON(&slaParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	evaluatedValue, err := evaluateSLA(contract, slaParams)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.IndentedJSON(http.StatusOK, evaluatedValue)

}

func readSLA(contract *client.Contract, mowerID string) (*SLA, error) {
	fmt.Printf("\n--> Evaluate Transaction: ReadSLA, function returns key value pair\n")

	evaluateResult, err := contract.EvaluateTransaction("ReadSLA", mowerID)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
		return nil, err
	}

	var sla SLA
	json.Unmarshal(evaluateResult, &sla)
	fmt.Println("Result: ", string(evaluateResult[:]))
	return &sla, nil
}

func ReadSLAHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "mower"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	mowerID := c.Param("id")
	contract := network.GetContract(chaincodeName)
	sla, err := readSLA(contract, mowerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.IndentedJSON(http.StatusOK, sla)

}

func GetServiceLevelHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "mower"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	mowerID := c.Param("id")
	contract := network.GetContract(chaincodeName)
	sla, err := readSLA(contract, mowerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.IndentedJSON(http.StatusOK, sla.ServiceLevel)

}

// Evaluate a transaction by key to query ledger state.
func readCustomer(contract *client.Contract, customerID string) (*Customer, error) {
	fmt.Printf("\n--> Evaluate Transaction: Read, function returns key value pair\n")

	evaluateResult, err := contract.EvaluateTransaction("ReadCustomer", customerID)
	if err != nil {
		return nil, (fmt.Errorf("failed to evaluate transaction: %w", err))
	}

	fmt.Println("Result: ", string(evaluateResult[:]))
	var customer Customer
	json.Unmarshal(evaluateResult, &customer)
	return &customer, nil
}

func ReadCustomerHandler(c *gin.Context) {
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}

	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "customer"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	// chaincodeName2 := "bumpy"

	channelName := "customer"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)

	contract := network.GetContract(chaincodeName)
	customerID := c.Param("id")
	customer, err := readCustomer(contract, customerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.IndentedJSON(http.StatusOK, customer)
}

func getCustomerSLAs(contract *client.Contract, customerID string) {
	fmt.Printf("\n--> Evaluate Transaction: getCustomerSLAs\n")

	submitResult, err := contract.SubmitTransaction("GetAllSLA", customerID)
	if err != nil {
		switch err := err.(type) {
		case *client.EndorseError:
			fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.SubmitError:
			fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		case *client.CommitStatusError:
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
			} else {
				fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
			}
		case *client.CommitError:
			fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
		default:
			panic(fmt.Errorf("unexpected error type %T: %w", err, err))
		}

		// Any error that originates from a peer or orderer node external to the gateway will have its details
		// embedded within the gRPC status error. The following code shows how to extract that.
		statusErr := status.Convert(err)

		details := statusErr.Details()
		if len(details) > 0 {
			fmt.Println("Error Details:")

			for _, detail := range details {
				switch detail := detail.(type) {
				case *gateway.ErrorDetail:
					fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
				}
			}
		}
	}

	fmt.Println("Result: ", string(submitResult[:]))
}

// Submit transaction, passing in the wrong number of arguments ,expected to throw an error containing details of any error responses from the smart contract.
func exampleErrorHandling(contract *client.Contract) {
	fmt.Println("\n--> Submit Transaction: UpdateAsset asset70, asset70 does not exist and should return an error")

	_, err := contract.SubmitTransaction("UpdateAsset", "asset70", "blue", "5", "Tomoko", "300")
	if err == nil {
		panic("******** FAILED to return an error")
	}

	fmt.Println("*** Successfully caught the error:")

	switch err := err.(type) {
	case *client.EndorseError:
		fmt.Printf("Endorse error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
	case *client.SubmitError:
		fmt.Printf("Submit error for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
	case *client.CommitStatusError:
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Printf("Timeout waiting for transaction %s commit status: %s", err.TransactionID, err)
		} else {
			fmt.Printf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", err.TransactionID, status.Code(err), err)
		}
	case *client.CommitError:
		fmt.Printf("Transaction %s failed to commit with status %d: %s\n", err.TransactionID, int32(err.Code), err)
	default:
		panic(fmt.Errorf("unexpected error type %T: %w", err, err))
	}

	// Any error that originates from a peer or orderer node external to the gateway will have its details
	// embedded within the gRPC status error. The following code shows how to extract that.
	statusErr := status.Convert(err)

	details := statusErr.Details()
	if len(details) > 0 {
		fmt.Println("Error Details:")

		for _, detail := range details {
			switch detail := detail.(type) {
			case *gateway.ErrorDetail:
				fmt.Printf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message)
			}
		}
	}
}

// Format JSON data
func formatJSON(data []byte) string {
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, data, "", "  "); err != nil {
		panic(fmt.Errorf("failed to parse JSON: %w", err))
	}
	return prettyJSON.String()
}
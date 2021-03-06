package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/cmiclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	secretFilename       string
	machineclassFilename string
	classKind            string
	machineName          string
	machineID            string
)

// func NewDriver(machineID string, secretRef *corev1.Secret, classKind string, machineClass interface{}, machineName string) Driver {

// CreateFlags adds flags for a specific CMServer to the specified FlagSet
func CreateFlags() {

	flag.StringVar(&secretFilename, "secret", "", "infrastructure secret")
	flag.StringVar(&machineclassFilename, "machineclass", "", "infrastructure machineclass")
	flag.StringVar(&classKind, "classkind", "", "infrastructure class kind")
	flag.StringVar(&machineName, "machinename", "", "machine name")
	flag.StringVar(&machineID, "machineid", "", "machine id")

	flag.Parse()
}

func main() {

	CreateFlags()

	var (
		machineclass interface{}
	)

	if machineName == "" {
		log.Fatalf("machine name required")
	}

	if machineclassFilename == "" {
		log.Fatalf("machine class filename required")
	}

	if secretFilename == "" {
		log.Fatalf("secret filename required")
	}
	secret := corev1.Secret{}
	err := Read(secretFilename, &secret)
	if err != nil {
		log.Fatalf("Could not parse secret yaml: %s", err)
	}

	switch classKind {
	case "MachineClass", "machineclass":
		class := v1alpha1.MachineClass{}
		machineclass = &class
		classKind = "MachineClass"
	default:
		log.Fatalf("Unknown class kind %s", classKind)
	}
	err = Read(machineclassFilename, machineclass)
	if err != nil {
		log.Fatalf("Could not parse machine class yaml: %s", err)
	}

	cmiclient, err := cmiclient.NewCMIPluginClient(machineID, classKind, &secret, machineclass, machineName, "")
	if err != nil {
		log.Fatalf("Couldn't create CMIDirver client : %s", err)
	}

	if machineID == "" {
		id, name, _, err := cmiclient.CreateMachine()
		if err != nil {
			log.Fatalf("Could not create %s : %s", machineName, err)
		}
		fmt.Printf("Machine id: %s\n", id)
		fmt.Printf("Name: %s\n", name)
	} else {
		_, err = cmiclient.DeleteMachine()
		if err != nil {
			log.Fatalf("Could not delete %s : %s", machineID, err)
		}
	}

}

// Read function decodes the yaml file passed to it
func Read(fileName string, decodedObj interface{}) error {
	m, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Fatalf("Could not read %s: %s", fileName, err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(m), 1024)

	return decoder.Decode(decodedObj)
}

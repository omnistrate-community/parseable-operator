/*
Copyright (c) 2024-2026 Parseable, Inc.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package controller

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func detectType(obj client.Object) string { return reflect.TypeOf(obj).String() }

// addOwnerRefToObject adds an owner reference to a Kubernetes object,
// which helps establish ownership and allows for garbage collection if the owner is deleted.
func addOwnerRefToObject(obj metav1.Object, ownerRef metav1.OwnerReference) {
	trueVar := true
	ownerRef = metav1.OwnerReference{
		APIVersion: ownerRef.APIVersion,
		Kind:       ownerRef.Kind,
		Name:       ownerRef.Name,
		UID:        ownerRef.UID,
		Controller: &trueVar,
	}
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), ownerRef))
}

// addHashToObject calculates a SHA-1 hash of the Kubernetes object and adds it as an annotation.
// This hash is used to detect changes between the desired and current state of the object.
func addHashToObject(obj client.Object, name string) error {
	// Generate a hash of the object
	if sha, err := getObjectHash(obj); err != nil {
		return err
	} else {
		// Add the hash to the object's annotations
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
			obj.SetAnnotations(annotations)
		}
		annotations[name] = sha
		return nil
	}
}

// getObjectHash computes a SHA-1 hash of the given Kubernetes object and returns it as a base64-encoded string.
// This function is used to create a unique identifier for the object's state.
func getObjectHash(obj client.Object) (string, error) {
	// Marshal the object to JSON
	if bytes, err := json.Marshal(obj); err != nil {
		return "", err
	} else {
		// Compute the SHA-1 hash of the JSON bytes
		sha1Bytes := sha1.Sum(bytes)
		// Return the base64-encoded hash
		return base64.StdEncoding.EncodeToString(sha1Bytes[:]), nil
	}
}

// returns pointer to bool
func boolFalse() *bool {
	bool := false
	return &bool
}

// pass slice of strings for namespaces
func getEnvAsSlice(name string, defaultVal []string, sep string) []string {
	valStr := getDenyListEnv(name, "")
	if valStr == "" {
		return defaultVal
	}
	// split on ","
	val := strings.Split(valStr, sep)
	return val
}

// lookup DENY_LIST, default is nil
func getDenyListEnv(key string, defaultVal string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultVal
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

// appendRandomChars appends 4 random alphanumeric characters to the input string.
func appendRandomChars(input string) (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789" // Lowercase and digits only
	var randomChars []byte

	for i := 0; i < 4; i++ {
		// Generate a random index
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}

		// Append the random character to the slice
		randomChars = append(randomChars, chars[index.Int64()])
	}

	// Append random characters to the input string
	return input + "-" + string(randomChars), nil
}

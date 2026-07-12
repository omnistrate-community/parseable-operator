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

package utils

import (
	"context"
	"crypto/rand"
	"math/big"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VerbType string

type (
	TransformStatusFunc func(obj client.Object) client.Object
)

const (
	VerbPatched   VerbType = "Patched"
	VerbUnchanged VerbType = "Unchanged"
)

// AppendRandomChars appends 4 random alphanumeric characters to the input string.
func AppendRandomChars(input string) (string, error) {
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

func PatchStatus(ctx context.Context, c client.Client, obj client.Object, transform TransformStatusFunc, opts ...client.SubResourcePatchOption) (client.Object, VerbType, error) {
	key := types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
	err := c.Get(ctx, key, obj)
	if err != nil {
		return nil, VerbUnchanged, err
	}

	// The body of the request was in an unknown format -
	// accepted media types include:
	//   - application/json-patch+json,
	//   - application/merge-patch+json,
	//   - application/apply-patch+yaml
	patch := client.MergeFrom(obj)
	obj = transform(obj.DeepCopyObject().(client.Object))
	err = c.Status().Patch(ctx, obj, patch, opts...)
	if err != nil {
		return nil, VerbUnchanged, err
	}

	return obj, VerbPatched, nil
}

func PatchObj(ctx context.Context, c client.Client, obj client.Object, transform TransformStatusFunc, opts ...client.PatchOption) (client.Object, VerbType, error) {
	key := types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
	err := c.Get(ctx, key, obj)
	if err != nil {
		return nil, VerbUnchanged, err
	}

	// The body of the request was in an unknown format -
	// accepted media types include:
	//   - application/json-patch+json,
	//   - application/merge-patch+json,
	//   - application/apply-patch+yaml
	patch := client.MergeFrom(obj)
	obj = transform(obj.DeepCopyObject().(client.Object))
	err = c.Patch(ctx, obj, patch, opts...)
	if err != nil {
		return nil, VerbUnchanged, err
	}
	return obj, VerbPatched, nil
}

func RemoveLabel(labels map[string]string, key string) {
	delete(labels, key)
}

func MutateString(existingName string) (string, error) {
	trimmedString := existingName[:len(existingName)-5]
	newString, err := AppendRandomChars(trimmedString)
	if err != nil {
		return "", err
	}
	return newString, nil
}

func MergeLabels(currentLabels, existingLabels map[string]string) map[string]string {

	for key, value := range existingLabels {
		currentLabels[key] = value
	}

	return currentLabels
}

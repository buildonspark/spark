package common

import "google.golang.org/protobuf/proto"

func getAny[K comparable, V any](m map[K]V) (K, V) {
	for k, v := range m {
		return k, v
	}
	// Handle empty map case
	var k K
	var v V
	return k, v
}

// MapOfArrayToArrayOfMap converts a map of K to an array of V to an array of maps of K to V.
//
// Example:
// MapOfArrayToArrayOfMap(map[string][]int{"a": {1, 2}, "b": {3, 4}})
// Returns: []map[string]int{{"a": 1, "b": 3}, {"a": 2, "b": 4}}
func MapOfArrayToArrayOfMap[K comparable, V any](mapOfArray map[K][]V) []map[K]V {
	_, arrObject := getAny(mapOfArray)
	results := make([]map[K]V, len(arrObject))
	for i := range results {
		results[i] = make(map[K]V)
	}
	for k, v := range mapOfArray {
		for i, value := range v {
			results[i][k] = value
		}
	}
	return results
}

// ArrayOfMapToArrayOfMap converts an array of maps of K to V to a map of K to an array of V.
//
// Example:
// ArrayOfMapToArrayOfMap([]map[string]int{{"a": 1, "b": 3}, {"a": 2, "b": 4}})
// Returns: map[string][]int{"a": {1, 2}, "b": {3, 4}}
func ArrayOfMapToMapOfArray[K comparable, V any](arrayOfMap []map[K]V) map[K][]V {
	results := make(map[K][]V)
	for _, m := range arrayOfMap {
		for k, v := range m {
			results[k] = append(results[k], v)
		}
	}
	return results
}

// SwapMapKeys swaps the keys of a map of maps.
//
// Example:
// map[string]map[int]string{"a": {1: "b", 2: "c"}, "d": {1: "e", 2: "f"}}
// Returns: map[int]map[string]string{{1: {"a": "b", "d": "e"}, 2: {"a": "c", "d": "f"}}}
func SwapMapKeys[K1 comparable, K2 comparable, V any](m map[K1]map[K2]V) map[K2]map[K1]V {
	results := make(map[K2]map[K1]V)
	for k1, v1 := range m {
		for k2, v2 := range v1 {
			if _, ok := results[k2]; !ok {
				results[k2] = make(map[K1]V)
			}
			results[k2][k1] = v2
		}
	}
	return results
}

// ConvertObjectMapToProtoMap converts a map of V to a map of T, where V is a ProtoConvertable[T].
func ConvertObjectMapToProtoMap[K comparable, V ProtoConvertable[T], T proto.Message](m map[K]V) (map[K]T, error) {
	results := make(map[K]T)
	for k, v := range m {
		proto, err := v.MarshalProto()
		if err != nil {
			return nil, err
		}
		results[k] = proto
	}
	return results, nil
}

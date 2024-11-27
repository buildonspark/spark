package common

func getAny[K comparable, V any](m map[K]V) (K, V) {
	for k, v := range m {
		return k, v
	}
	// Handle empty map case
	var k K
	var v V
	return k, v
}

func MapOfArrayToArrayOfMap[K comparable, V any](mapOfArray map[K][]V) []map[K]V {
	_, arrObject := getAny(mapOfArray)
	results := make([]map[K]V, len(arrObject))
	for i, _ := range results {
		results[i] = make(map[K]V)
	}
	for k, v := range mapOfArray {
		for i, value := range v {
			results[i][k] = value
		}
	}
	return results
}

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

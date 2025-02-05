/*
DBSCAN (Density-based spatial clustering) clustering optimized for multicore processing.

Usage example:

	var clusterer = NewDBSCANClusterer( 2.0, 2 )

	var data = []ClusterablePoint{
			&NamedPoint{"0", []float64{2, 4}},
			&NamedPoint{"1", []float64{7, 3}},
			&NamedPoint{"2", []float64{3, 5}},
			&NamedPoint{"3", []float64{5, 3}},
			&NamedPoint{"4", []float64{7, 4}},
		}

	clusterer.MinPts = 2
	clusterer.SetEps( 2.0 )

	// Automatic discovery of dimension with max variance
	clusterer.AutoSelectDimension = false
	// Set dimension manually
	clusterer.SortDimensionIndex = 1

	var result  [][]ClusterablePoint = clusterer.Cluster(data)
*/
package dbscan

import (
	"container/list"
	"fmt"
	"math"
	"sync"
)

type Clusterer interface {
	Cluster([]ClusterablePoint) [][]ClusterablePoint
}

type DBSCANClusterer struct {
	eps, eps2                                 float64
	MinPts, numDimensions, SortDimensionIndex int
	AutoSelectDimension                       bool
}

func NewDBSCANClusterer(
	eps float64,
	minPts int,
) *DBSCANClusterer {
	return &DBSCANClusterer{
		eps:    eps,
		eps2:   eps * eps,
		MinPts: minPts,

		AutoSelectDimension: false,
	}
}

func (this *DBSCANClusterer) GetEps() float64 {
	return this.eps
}
func (this *DBSCANClusterer) SetEps(eps float64) {
	this.eps = eps
	this.eps2 = eps * eps
}

/*
*
step 1: sort data by a dimension
step 2: slide through sorted data (in parallel), and compute all points in range of eps (everything above eps is definitely isn't directly reachable)
step 3: build neighborhood map & proceed DFS
*
*/
func (this *DBSCANClusterer) Cluster(data []ClusterablePoint) [][]ClusterablePoint {
	if len(data) == 0 {
		return [][]ClusterablePoint{}
	}
	var (
		dataSize   = len(data)
		clusters   = make([][]ClusterablePoint, 0, 64)
		visitedMap = make([]bool, dataSize)
		cluster    = make([]ClusterablePoint, 0, 64)

		neighborhoodMap []*ConcurrentQueue_InsertOnly
	)

	this.numDimensions = len(data[0].GetPoint())

	if this.AutoSelectDimension {
		this.SortDimensionIndex = this.PredictDimensionByMaxVariance(data)
	} else {
		this.SortDimensionIndex = len(data[0].GetPoint()) - 1
	}

	ClusterablePointSlice{
		Data:          data,
		SortDimension: this.SortDimensionIndex,
	}.Sort()

	neighborhoodMap = this.BuildNeighborhoodMap(data)

	// Early exit - 1 huge cluster
	if neighborhoodMap[0].Size == uint64(dataSize) {
		cluster = make([]ClusterablePoint, 0, dataSize)

		for _, v := range neighborhoodMap[0].Slice() {
			cluster = append(cluster, data[v])
		}

		clusters = append(clusters, cluster)
		return clusters
	}

	var (
		queue = list.New()
		elem  *list.Element
	)

	for pointIndex, tmpIndex := 0, uint(0); pointIndex < dataSize; pointIndex += 1 {
		if visitedMap[pointIndex] {
			continue
		}
		// Expand cluster
		queue.PushBack(uint(pointIndex))

		// DFS
		for queue.Len() > 0 {
			// Pop last elem
			elem = queue.Back()
			queue.Remove(elem)

			tmpIndex = elem.Value.(uint)
			if visitedMap[tmpIndex] {
				continue
			}

			cluster = append(cluster, data[tmpIndex])
			visitedMap[tmpIndex] = true

			for _, v := range neighborhoodMap[tmpIndex].Slice() {
				queue.PushBack(v)
			}
		}

		if len(cluster) >= this.MinPts {
			clusters = append(clusters, cluster)
		}

		cluster = make([]ClusterablePoint, 0, 64)
	}
	return clusters
}
func normalize(vec []float64) []float64 {
	var (
		sum = 0.0
	)
	for _, v := range vec {
		sum += v * v
	}
	sum = math.Sqrt(sum)
	for i, v := range vec {
		vec[i] = v / sum
	}
	return vec
}
func (this *DBSCANClusterer) CalcDistance(aPoint, bPoint []float64) float64 {
	var sum = 0.0
	//aPoint = normalize(aPoint)
	//bPoint = normalize(bPoint)

	for i, size := 0, this.numDimensions; i < size; i += 1 {
		x := aPoint[i] - bPoint[i]
		sum += x * x
	}
	return math.Sqrt(sum)

}

func (this *DBSCANClusterer) CalcDistanceCosine(aPoint, bPoint []float64) float64 {
	cosineSimilarity, err := cosineSimilarity(aPoint, bPoint)
	if err != nil {
		return 1.0 // 180 degrees
	}
	return 1.0 - cosineSimilarity
}

func (this *DBSCANClusterer) BuildNeighborhoodMap(data []ClusterablePoint) []*ConcurrentQueue_InsertOnly {
	var (
		dataSize  = len(data)
		result    = make([]*ConcurrentQueue_InsertOnly, dataSize)
		waitGroup = new(sync.WaitGroup)

		fn = func(start int) {
			defer waitGroup.Done()
			var (
				x, head ClusterablePoint = nil, data[start]

				headV []float64 = head.GetPoint()
				//headDimV float64   = headV[this.SortDimensionIndex] + 10.0 //this.eps
			)
			if result[start] == nil {
				result[start] = NewConcurrentQueue_InsertOnly()
			}
			result[start].Add(uint(start))

			for i := start + 1; i < dataSize; i += 1 { // && data[i].GetPoint()[this.SortDimensionIndex] <= headDimV
				x = data[i]

				if this.CalcDistanceCosine(headV, x.GetPoint()) <= this.eps {
					result[start].Add(uint(i))
					if result[i] == nil {
						result[i] = NewConcurrentQueue_InsertOnly()
					}
					result[i].Add(uint(start))
				}
			}
		}
	)
	waitGroup.Add(dataSize)

	// Early exit - 1 huge cluster
	fn(0)

	if result[0].Size == uint64(dataSize) {
		return result
	}

	for i := 1; i < dataSize; i += 1 {
		go fn(i)
	}
	waitGroup.Wait()

	return result
}

/**
 * Calculate variance for each dimension (in parallel), returns dimension index with max variance
 */
func (this *DBSCANClusterer) PredictDimensionByMaxVariance(data []ClusterablePoint) int {
	var (
		waitGroup = new(sync.WaitGroup)
		result    = make([]float64, this.numDimensions)
	)
	waitGroup.Add(int(this.numDimensions))

	for i, size := 0, this.numDimensions; i < size; i += 1 {
		go func(dim int) {
			result[dim] = Variance(data, dim)
			waitGroup.Done()
		}(i)
	}

	waitGroup.Wait()

	var (
		maxV = 0.0
		maxI = 0
	)
	for i, v := range result {
		if maxV <= v {
			maxV = v
			maxI = i
		}
	}
	return maxI
}

func Variance(
	data []ClusterablePoint,
	dimension int,
) float64 {
	var (
		size     = len(data)
		avg      = 0.0
		sum      = 0.0
		delta, v float64
	)
	if size < 2 {
		return 0.0
	}
	for i, point := range data {
		v = point.GetPoint()[dimension]
		delta = v - avg
		avg += delta / float64(i+1)
		sum += delta * (v - avg)
	}
	return sum / float64(size-1)
}

// DotProduct returns the dot product of two vectors.
func dotProduct(x, y []float64) (
	float64,
	error,
) {
	if len(x) != len(y) {
		return 0, fmt.Errorf("x and y have unequal lengths: %d / %d", len(x), len(y))
	}

	p := make([]float64, len(x))
	sum := 0.0
	for i, _ := range x {
		p[i] = x[i] * y[i]
		sum = sum + p[i]
	}
	return sum, nil
}

// Norm returns the vector norm.  Use pow = 2.0 for Euclidean.
func norm(
	x []float64,
	pow float64,
) float64 {
	s := 0.0

	for _, xval := range x {
		s += math.Pow(xval, pow)
	}

	return math.Pow(s, 1/pow)
}

// Cosine returns the cosine similarity between two vectors.
func cosineSimilarity(x, y []float64) (
	float64,
	error,
) {
	d, err := dotProduct(x, y)
	if err != nil {
		return 0.0, err
	}

	xnorm := norm(x, 2.0)
	ynorm := norm(y, 2.0)

	return d / (xnorm * ynorm), nil
}

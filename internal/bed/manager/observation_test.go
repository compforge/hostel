package manager

import (
	"sync"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
)

func observationFixture() (*Manager, *managedBed, *bedInitialization) {
	b := model.New("observed", "bed-observed", model.Spec{})
	resident := &managedBed{Bed: b}
	initialization := &bedInitialization{model: b}
	m := &Manager{
		owners:          model.NewOwners(),
		beds:            map[string]*managedBed{},
		initializations: map[string]*bedInitialization{},
		retirements:     map[string]*managedBed{},
		purges:          map[string]*bedPurge{},
	}
	return m, resident, initialization
}

func TestObserveBedLifecycle(t *testing.T) {
	for _, phase := range []Phase{PhaseInitializing, PhaseResident, PhaseEvicting, PhasePurging, PhaseFailed} {
		t.Run(string(phase), func(t *testing.T) {
			m, resident, initialization := observationFixture()
			name := resident.Name
			m.setLifecycle(resident.Bed, model.LifecycleStatus{Phase: phase, UpdatedAt: time.Now()})
			switch phase {
			case PhaseResident:
				m.beds[name] = resident
			case PhaseEvicting:
				m.retirements[name] = resident
			case PhasePurging:
				m.purges[name] = &bedPurge{status: InitializationStatus{Name: name, BedStatus: BedStatus{Phase: phase}}}
			default:
				m.initializations[name] = initialization
			}
			got, ok := m.ObserveBed(name)
			if !ok {
				t.Fatal("declared Bed disappeared")
			}
			if phase == PhaseResident {
				if got.Resident != resident || got.Model != resident.Bed {
					t.Fatalf("resident observation = %+v", got)
				}
			} else {
				if got.Resident != nil || got.Initialization.Phase != phase {
					t.Fatalf("transition observation = %+v", got)
				}
				// A purge fence can outlive the in-memory Bed model.
				if phase != PhasePurging && got.Model != resident.Bed {
					t.Fatal("transition lost its identity")
				}
			}
			if _, exists := m.ObserveBed("missing"); exists {
				t.Fatal("unknown Bed exists")
			}
		})
	}
}

// +case:publication=`A publication between the legacy resident and initialization lookups produces two misses; one observation must return either side of the transition.`
func TestObserveBedAcrossPublication(t *testing.T) {
	m, resident, initialization := observationFixture()
	name := resident.Name
	m.setLifecycle(resident.Bed, model.LifecycleStatus{Phase: PhaseInitializing})
	m.initializations[name] = initialization
	before, ok := m.ObserveBed(name)
	if !ok || before.Model != resident.Bed || before.Initialization.Phase != PhaseInitializing {
		t.Fatalf("before publication = %+v, found=%t", before, ok)
	}
	if _, exists := m.Get(name); exists {
		t.Fatal("Bed already resident")
	}
	// Force the publication precisely between the two old queries, without sleeps.
	m.mu.Lock()
	m.setLifecycle(resident.Bed, model.LifecycleStatus{Phase: PhaseResident, Ready: true})
	m.beds[name] = resident
	delete(m.initializations, name)
	m.mu.Unlock()
	if _, exists := m.Initialization(name); exists {
		t.Fatal("published Bed still initializing")
	}
	after, ok := m.ObserveBed(name)
	if !ok || after.Resident != resident || after.Model != before.Model {
		t.Fatalf("after publication = %+v, found=%t", after, ok)
	}
	if before.Initialization.Phase != PhaseInitializing {
		t.Fatal("publication mutated a captured lifecycle snapshot")
	}
}

func TestObserveBedConcurrentTransitions(t *testing.T) {
	m, resident, initialization := observationFixture()
	name := resident.Name
	m.initializations[name] = initialization
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 2000 {
				got, ok := m.ObserveBed(name)
				if !ok || got.Model != resident.Bed {
					t.Errorf("Bed disappeared during transition: %+v, found=%t", got, ok)
					return
				}
			}
		})
	}
	for range 2000 {
		m.mu.Lock()
		m.beds[name] = resident
		delete(m.initializations, name)
		m.mu.Unlock()
		m.mu.Lock()
		m.retirements[name] = resident
		delete(m.beds, name)
		m.mu.Unlock()
		m.mu.Lock()
		m.initializations[name] = initialization
		delete(m.retirements, name)
		m.mu.Unlock()
	}
	readers.Wait()
}

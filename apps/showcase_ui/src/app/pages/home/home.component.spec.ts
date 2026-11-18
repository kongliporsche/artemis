/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { of } from 'rxjs';

import { AgentService, TaskDevice } from '../../services/agent.service';
import { SystemService } from '../../services/system.service';
import { TaskRecommendationService } from '../../core/services/task-recommendation.service';
import { HomeComponent } from './home.component';

describe('HomeComponent setup', () => {
  const storageKey = 'artemis_model_setup_mode';
  let systemService: {
    apiKeysMap: ReturnType<typeof signal<Record<string, string>>>;
    currentApiKey: ReturnType<typeof signal<string>>;
    setSkipCredentialsCheck: jasmine.Spy;
    fetchReadiness: jasmine.Spy;
    fetchModelConfigEnv: jasmine.Spy;
    fetchAdbServerStatus: jasmine.Spy;
    pythonProbe: jasmine.Spy;
    configProbe: jasmine.Spy;
    isReady: jasmine.Spy;
    isCredentialsReady: jasmine.Spy;
  };

  beforeEach(async () => {
    localStorage.removeItem(storageKey);
    systemService = {
      apiKeysMap: signal<Record<string, string>>({}),
      currentApiKey: signal(''),
      setSkipCredentialsCheck: jasmine.createSpy('setSkipCredentialsCheck'),
      fetchReadiness: jasmine.createSpy('fetchReadiness').and.returnValue(of(null)),
      fetchModelConfigEnv: jasmine.createSpy('fetchModelConfigEnv').and.returnValue(of(null)),
      fetchAdbServerStatus: jasmine.createSpy('fetchAdbServerStatus').and.returnValue(
        of({ endpoint: { mode: 'local' } })
      ),
      pythonProbe: jasmine.createSpy('pythonProbe').and.returnValue({ status: 'pass' }),
      configProbe: jasmine.createSpy('configProbe').and.returnValue({ status: 'pass' }),
      isReady: jasmine.createSpy('isReady').and.returnValue(false),
      isCredentialsReady: jasmine.createSpy('isCredentialsReady').and.returnValue(true)
    };

    await TestBed.configureTestingModule({
      imports: [HomeComponent],
      providers: [
        provideRouter([]),
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: AgentService, useValue: {
          getProTuningDefaults: () => of({}),
          listTaskDevices: () => of({ devices: [], ios_usb_support: 'helper_missing' })
        } },
        { provide: SystemService, useFactory: () => Object.assign(new SystemService(), systemService) },
        { provide: TaskRecommendationService, useValue: { filterAndRankTasks: () => [] } }
      ]
    }).compileComponents();
  });

  afterEach(() => localStorage.removeItem(storageKey));

  it('persists and restores the custom setup choice', () => {
    const first = TestBed.createComponent(HomeComponent);
    first.componentInstance.setModelSetupMode('custom');

    expect(localStorage.getItem(storageKey)).toBe('custom');
    first.destroy();

    const second = TestBed.createComponent(HomeComponent);
    second.componentInstance.ngOnInit();

    expect(second.componentInstance.modelSetupMode()).toBe('custom');
    expect(systemService.setSkipCredentialsCheck).toHaveBeenCalledWith(true);
    second.destroy();
  });

  it('opens iOS setup from the launcher even when no device can be discovered', () => {
    const fixture = TestBed.createComponent(HomeComponent);
    fixture.detectChanges();

    const page: HTMLElement = fixture.nativeElement;
    const setup = Array.from(page.querySelectorAll('button')).find(button => button.textContent.includes('Set up iOS USB'));
    expect(setup).toBeDefined();
    setup?.click();
    fixture.detectChanges();

    expect(page.textContent).toContain('iOS USB Device Connection');
    expect(page.textContent).toContain('Artemis iOS helper not found');
    expect(page.textContent).not.toContain('Android Platform-Tools');
  });

  it('carries the chosen USB iPhone to the launcher without requiring Android readiness', () => {
    const component = TestBed.createComponent(HomeComponent).componentInstance;
    const phone: TaskDevice = { serial: 'ios:test', platform: 'ios', state: 'device', model: 'iPhone', is_busy: false };
    spyOn(TestBed.inject(AgentService), 'listTaskDevices').and.returnValue(
      of({ devices: [phone], ios_usb_support: 'available' })
    );

    component.openIosSetup();
    expect(component.isSetupReady()).toBeTrue();
    component.launchFromSetup(phone.serial);

    expect(component.activeTab()).toBe('launcher');
    expect(component.selectedTaskSerial()).toBe(phone.serial);
    expect(component.canLaunchTask()).toBeTrue();
    expect(systemService.isReady).not.toHaveBeenCalled();
  });
});

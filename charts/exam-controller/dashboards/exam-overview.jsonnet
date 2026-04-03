local g = import 'github.com/grafana/grafonnet/gen/grafonnet-latest/main.libsonnet';

local ds = '${DS_PROMETHEUS}';
local filters = 'exam="$exam", namespace="$namespace"';

// --- Helper: Prometheus query target ---
local promQuery(expr, legendFormat='', refId='A') =
  g.query.prometheus.new(ds, expr)
  + g.query.prometheus.withLegendFormat(legendFormat)
  + g.query.prometheus.withRefId(refId);

// --- Helper: Color override by name ---
local colorOverride(name, color) =
  g.panel.stat.standardOptions.override.byName.new(name)
  + g.panel.stat.standardOptions.override.byName.withProperty('color', {
    fixedColor: color,
    mode: 'fixed',
  });

// --- Helper: Threshold override by name ---
local thresholdOverride(name, steps) =
  g.panel.stat.standardOptions.override.byName.new(name)
  + g.panel.stat.standardOptions.override.byName.withProperty('thresholds', {
    mode: 'absolute',
    steps: steps,
  });

// --- Helper: Base stat panel ---
local statPanel(title, targets, overrides=[], textMode='auto', unit=null) =
  g.panel.stat.new(title)
  + g.panel.stat.queryOptions.withDatasource('prometheus', ds)
  + g.panel.stat.queryOptions.withTargets(targets)
  + g.panel.stat.options.withColorMode('value')
  + g.panel.stat.options.withGraphMode('none')
  + g.panel.stat.options.withJustifyMode('auto')
  + g.panel.stat.options.withOrientation('auto')
  + g.panel.stat.options.reduceOptions.withCalcs(['last'])
  + g.panel.stat.options.reduceOptions.withFields('')
  + g.panel.stat.options.reduceOptions.withValues(false)
  + g.panel.stat.options.withTextMode(textMode)
  + g.panel.stat.standardOptions.color.withMode('thresholds')
  + g.panel.stat.standardOptions.thresholds.withMode('absolute')
  + g.panel.stat.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + (if unit != null then g.panel.stat.standardOptions.withUnit(unit) else {})
  + (if std.length(overrides) > 0 then g.panel.stat.standardOptions.withOverrides(overrides) else {})
  + { options+: { noValue: 'N/A' } };

// --- Helper: Query variable ---
local queryVar(name, label, queryStr) =
  g.dashboard.variable.query.new(name)
  + g.dashboard.variable.query.withDatasource('prometheus', ds)
  + g.dashboard.variable.query.generalOptions.withLabel(label)
  + g.dashboard.variable.query.selectionOptions.withMulti(false)
  + g.dashboard.variable.query.selectionOptions.withIncludeAll(false)
  + g.dashboard.variable.query.refresh.onTime()
  + g.dashboard.variable.query.withSort(1)
  + g.dashboard.variable.query.withRegex('')
  + {
    skipUrlSync: false,
    current: {},
    options: [],
    definition: queryStr,
    query: {
      qryType: 1,
      query: queryStr,
      refId: 'PrometheusVariableQueryEditor-VariableQuery',
    },
  };

local variables = [
  queryVar('namespace', 'Namespace', 'label_values(exam_instances_total, namespace)'),
  queryVar('exam', 'Exam', 'label_values(exam_instances_total{namespace="$namespace"}, exam)'),
];

// ==========================================================================
// Row 1: Status at a Glance
// ==========================================================================

local phasePanel =
  statPanel(
    'Phase',
    [promQuery('exam_phase_duration_seconds{%(f)s} > 0' % { f: filters }, '{{phase}}')],
    overrides=[
      colorOverride('Ready', 'green'),
      colorOverride('Unlocked', 'green'),
      colorOverride('Provisioning', 'yellow'),
      colorOverride('Pending', 'blue'),
      colorOverride('Locked', 'orange'),
      colorOverride('TearingDown', 'red'),
    ],
    textMode='name',
  )
  + g.panel.stat.panelOptions.withDescription('Current exam lifecycle phase')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=0, y=1)
  + { id: 1 };

local healthyTotalPanel =
  statPanel(
    'Healthy / Total',
    [
      promQuery('exam_instances_healthy{%(f)s}' % { f: filters }, 'Healthy'),
      promQuery('exam_instances_total{%(f)s}' % { f: filters }, 'Total', refId='B'),
    ],
    overrides=[
      thresholdOverride('Total', [{ color: 'green', value: null }]),
    ],
    textMode='value_and_name',
  )
  + g.panel.stat.panelOptions.withDescription('Instances passing health checks vs total expected')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=4, y=1)
  + { id: 2 };

local failedPanel =
  statPanel(
    'Failed',
    [promQuery('exam_instances_failed{%(f)s}' % { f: filters })],
  )
  + g.panel.stat.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
    { color: 'red', value: 1 },
  ])
  + g.panel.stat.panelOptions.withDescription('Student instances in failed state')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=8, y=1)
  + { id: 14 };

local emailsPanel =
  statPanel(
    'Emails',
    [
      promQuery('exam_emails_sent_total{%(f)s}' % { f: filters }, 'Sent'),
      promQuery('exam_emails_failed_total{%(f)s}' % { f: filters }, 'Failed', refId='B'),
    ],
    overrides=[
      thresholdOverride('Failed', [
        { color: 'green', value: null },
        { color: 'red', value: 1 },
      ]),
    ],
    textMode='value_and_name',
  )
  + g.panel.stat.panelOptions.withDescription('Credential emails sent and failed')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=12, y=1)
  + { id: 3 };

local dryRunPanel =
  statPanel(
    'Dry Run',
    [
      promQuery('exam_dryrun_passed{%(f)s}' % { f: filters }, 'Passed'),
      promQuery('exam_dryrun_failed{%(f)s}' % { f: filters }, 'Failed', refId='B'),
    ],
    overrides=[
      thresholdOverride('Failed', [
        { color: 'green', value: null },
        { color: 'red', value: 1 },
      ]),
    ],
    textMode='value_and_name',
  )
  + g.panel.stat.panelOptions.withDescription('Pre-exam smoke test results — all should pass before unlock')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=16, y=1)
  + { id: 4 };

local countdownPanel =
  statPanel(
    'Countdowns',
    [
      promQuery('exam_seconds_until_unlock{%(f)s}' % { f: filters }, 'Unlock'),
      promQuery('exam_seconds_until_lock{%(f)s}' % { f: filters }, 'Lock', refId='B'),
    ],
    unit='dthms',
    textMode='value_and_name',
  )
  + g.panel.stat.panelOptions.withDescription('Countdown to exam start and end')
  + g.panel.stat.panelOptions.withGridPos(h=4, w=4, x=20, y=1)
  + { id: 5 };

// ==========================================================================
// Row 2: Provisioning & Health
// ==========================================================================

local instanceHealthPanel =
  g.panel.timeSeries.new('Instance Health Over Time')
  + g.panel.timeSeries.queryOptions.withDatasource('prometheus', ds)
  + g.panel.timeSeries.queryOptions.withTargets([
    promQuery('exam_instances_healthy{%(f)s}' % { f: filters }, 'Healthy'),
    promQuery('exam_instances_failed{%(f)s}' % { f: filters }, 'Failed', refId='B'),
    promQuery('exam_instances_total{%(f)s}' % { f: filters }, 'Total', refId='C'),
  ])
  + g.panel.timeSeries.standardOptions.color.withMode('palette-classic')
  + g.panel.timeSeries.standardOptions.thresholds.withMode('absolute')
  + g.panel.timeSeries.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + g.panel.timeSeries.fieldConfig.defaults.custom.withDrawStyle('line')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineInterpolation('linear')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineWidth(1)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withFillOpacity(10)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withPointSize(5)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withShowPoints('auto')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withSpanNulls(false)
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withMode('none')
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withGroup('A')
  + g.panel.timeSeries.standardOptions.withOverrides([
    g.panel.timeSeries.standardOptions.override.byName.new('Total')
    + g.panel.timeSeries.standardOptions.override.byName.withProperty('custom.fillOpacity', 0)
    + g.panel.timeSeries.standardOptions.override.byName.withProperty('custom.lineWidth', 2),
  ])
  + g.panel.timeSeries.options.legend.withDisplayMode('list')
  + g.panel.timeSeries.options.legend.withPlacement('bottom')
  + g.panel.timeSeries.options.legend.withShowLegend(true)
  + g.panel.timeSeries.options.tooltip.withMode('multi')
  + g.panel.timeSeries.options.tooltip.withSort('none')
  + g.panel.timeSeries.panelOptions.withDescription('Instance state over time')
  + g.panel.timeSeries.panelOptions.withGridPos(h=10, w=12, x=0, y=6)
  + { id: 7 };

local provisioningLatencyPanel =
  g.panel.heatmap.new('Provisioning Latency')
  + g.panel.heatmap.queryOptions.withDatasource('prometheus', ds)
  + g.panel.heatmap.queryOptions.withTargets([
    promQuery(
      'sum(rate(exam_provision_duration_seconds_bucket{%(f)s}[$__rate_interval])) by (le)' % { f: filters },
      '{{le}}'
    )
    + g.query.prometheus.withFormat('heatmap'),
  ])
  + g.panel.heatmap.panelOptions.withDescription('Only populated during the Provisioning phase. Blank outside that window is expected.')
  + {
    fieldConfig: {
      defaults: {
        color: { mode: 'scheme', schemeName: 'Oranges', steps: 128 },
        custom: {
          hideFrom: { legend: false, tooltip: false, viz: false },
          scaleDistribution: { type: 'linear' },
        },
      },
      overrides: [],
    },
    options: {
      calculate: false,
      cellGap: 1,
      color: {
        exponent: 0.5,
        fill: 'dark-orange',
        mode: 'scheme',
        reverse: false,
        scale: 'exponential',
        scheme: 'Oranges',
        steps: 128,
      },
      exemplars: { color: 'rgba(255,0,255,0.7)' },
      filterValues: { le: 1e-9 },
      legend: { show: true },
      rowsFrame: { layout: 'auto' },
      tooltip: { show: true, yHistogram: true },
      yAxis: { axisPlacement: 'left', reverse: false, unit: 's' },
    },
  }
  + g.panel.heatmap.panelOptions.withGridPos(h=10, w=6, x=12, y=6)
  + { id: 8 };

local phaseTimelinePanel =
  g.panel.stateTimeline.new('Phase Timeline')
  + g.panel.stateTimeline.queryOptions.withDatasource('prometheus', ds)
  + g.panel.stateTimeline.queryOptions.withTargets([
    promQuery('exam_phase_duration_seconds{%(f)s} > 0' % { f: filters }, '{{phase}}'),
  ])
  + g.panel.stateTimeline.standardOptions.color.withMode('fixed')
  + g.panel.stateTimeline.fieldConfig.defaults.custom.withFillOpacity(80)
  + g.panel.stateTimeline.fieldConfig.defaults.custom.withLineWidth(0)
  + g.panel.stateTimeline.standardOptions.thresholds.withMode('absolute')
  + g.panel.stateTimeline.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + g.panel.stateTimeline.standardOptions.withOverrides([
    colorOverride('Ready', 'green'),
    colorOverride('Unlocked', 'green'),
    colorOverride('Provisioning', 'yellow'),
    colorOverride('Pending', 'blue'),
    colorOverride('Locked', 'orange'),
    colorOverride('TearingDown', 'red'),
  ])
  + g.panel.stateTimeline.options.withAlignValue('left')
  + g.panel.stateTimeline.options.legend.withDisplayMode('list')
  + g.panel.stateTimeline.options.legend.withPlacement('bottom')
  + g.panel.stateTimeline.options.legend.withShowLegend(true)
  + g.panel.stateTimeline.options.withMergeValues(true)
  + g.panel.stateTimeline.options.withRowHeight(0.9)
  + g.panel.stateTimeline.options.withShowValue('auto')
  + g.panel.stateTimeline.options.tooltip.withMode('single')
  + g.panel.stateTimeline.options.tooltip.withSort('none')
  + g.panel.stateTimeline.panelOptions.withDescription('Exam lifecycle phase history')
  + g.panel.stateTimeline.panelOptions.withGridPos(h=10, w=6, x=18, y=6)
  + { id: 9 };

// ==========================================================================
// Row 3: Student Activity (collapsed, requires Hubble metrics)
// ==========================================================================

local hubbleFilters = 'destination_namespace="$namespace"';

local activeInstancesPanel =
  statPanel(
    'Active Instances',
    [promQuery(
      'count(rate(hubble_http_requests_total{%(f)s}[5m]) > 0)' % { f: hubbleFilters },
    )],
  )
  + g.panel.stat.standardOptions.thresholds.withSteps([
    { color: 'blue', value: null },
  ])
  + g.panel.stat.panelOptions.withDescription('Instances with HTTP activity in the last 5 minutes. Requires Hubble metrics.')
  + g.panel.stat.panelOptions.withGridPos(h=8, w=4, x=0, y=27)
  + { id: 20 };

local requestsPerInstancePanel =
  g.panel.timeSeries.new('Requests per Instance')
  + g.panel.timeSeries.queryOptions.withDatasource('prometheus', ds)
  + g.panel.timeSeries.queryOptions.withTargets([
    promQuery(
      'sum(rate(hubble_http_requests_total{%(f)s}[$__rate_interval])) by (destination_workload)' % { f: hubbleFilters },
      '{{destination_workload}}'
    ),
  ])
  + g.panel.timeSeries.standardOptions.color.withMode('palette-classic')
  + g.panel.timeSeries.standardOptions.withUnit('reqps')
  + g.panel.timeSeries.standardOptions.thresholds.withMode('absolute')
  + g.panel.timeSeries.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + g.panel.timeSeries.fieldConfig.defaults.custom.withDrawStyle('line')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineInterpolation('linear')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineWidth(1)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withFillOpacity(10)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withPointSize(5)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withShowPoints('auto')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withSpanNulls(false)
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withMode('none')
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withGroup('A')
  + g.panel.timeSeries.options.legend.withDisplayMode('list')
  + g.panel.timeSeries.options.legend.withPlacement('bottom')
  + g.panel.timeSeries.options.legend.withShowLegend(true)
  + g.panel.timeSeries.options.tooltip.withMode('multi')
  + g.panel.timeSeries.options.tooltip.withSort('none')
  + g.panel.timeSeries.panelOptions.withDescription('HTTP request rate per student instance. Requires Hubble metrics.')
  + g.panel.timeSeries.panelOptions.withGridPos(h=8, w=10, x=4, y=27)
  + { id: 21 };

local activityLatencyPanel =
  g.panel.heatmap.new('Request Latency')
  + g.panel.heatmap.queryOptions.withDatasource('prometheus', ds)
  + g.panel.heatmap.queryOptions.withTargets([
    promQuery(
      'sum(rate(hubble_http_request_duration_seconds_bucket{%(f)s}[$__rate_interval])) by (le)' % { f: hubbleFilters },
      '{{le}}'
    )
    + g.query.prometheus.withFormat('heatmap'),
  ])
  + g.panel.heatmap.panelOptions.withDescription('HTTP request latency distribution across student instances. Requires Hubble metrics.')
  + {
    fieldConfig: {
      defaults: {
        color: { mode: 'scheme', schemeName: 'Oranges', steps: 128 },
        custom: {
          hideFrom: { legend: false, tooltip: false, viz: false },
          scaleDistribution: { type: 'linear' },
        },
      },
      overrides: [],
    },
    options: {
      calculate: false,
      cellGap: 1,
      color: {
        exponent: 0.5,
        fill: 'dark-orange',
        mode: 'scheme',
        reverse: false,
        scale: 'exponential',
        scheme: 'Oranges',
        steps: 128,
      },
      exemplars: { color: 'rgba(255,0,255,0.7)' },
      filterValues: { le: 1e-9 },
      legend: { show: true },
      rowsFrame: { layout: 'auto' },
      tooltip: { show: true, yHistogram: true },
      yAxis: { axisPlacement: 'left', reverse: false, unit: 's' },
    },
  }
  + g.panel.heatmap.panelOptions.withGridPos(h=8, w=10, x=14, y=27)
  + { id: 22 };

// ==========================================================================
// Row 4: Operator Health (all exams) (collapsed)
// ==========================================================================

local reconcileLatencyPanel =
  g.panel.timeSeries.new('Reconcile Latency')
  + g.panel.timeSeries.queryOptions.withDatasource('prometheus', ds)
  + g.panel.timeSeries.queryOptions.withTargets([
    promQuery(
      'histogram_quantile(0.5, sum(rate(exam_reconcile_duration_seconds_bucket[$__rate_interval])) by (le))',
      'p50'
    ),
    promQuery(
      'histogram_quantile(0.99, sum(rate(exam_reconcile_duration_seconds_bucket[$__rate_interval])) by (le))',
      'p99',
      refId='B'
    ),
  ])
  + g.panel.timeSeries.standardOptions.color.withMode('palette-classic')
  + g.panel.timeSeries.standardOptions.withUnit('s')
  + g.panel.timeSeries.standardOptions.thresholds.withMode('absolute')
  + g.panel.timeSeries.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + g.panel.timeSeries.fieldConfig.defaults.custom.withDrawStyle('line')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineInterpolation('linear')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineWidth(1)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withFillOpacity(10)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withPointSize(5)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withShowPoints('auto')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withSpanNulls(false)
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withMode('none')
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withGroup('A')
  + g.panel.timeSeries.options.legend.withDisplayMode('list')
  + g.panel.timeSeries.options.legend.withPlacement('bottom')
  + g.panel.timeSeries.options.legend.withShowLegend(true)
  + g.panel.timeSeries.options.tooltip.withMode('multi')
  + g.panel.timeSeries.options.tooltip.withSort('none')
  + g.panel.timeSeries.panelOptions.withDescription('Controller-wide metric — not filtered by exam selection')
  + g.panel.timeSeries.panelOptions.withGridPos(h=8, w=8, x=0, y=17)
  + { id: 10 };

local reconcileErrorsPanel =
  g.panel.timeSeries.new('Reconcile Errors')
  + g.panel.timeSeries.queryOptions.withDatasource('prometheus', ds)
  + g.panel.timeSeries.queryOptions.withTargets([
    promQuery('rate(exam_reconcile_errors_total[$__rate_interval])', 'errors/s'),
  ])
  + g.panel.timeSeries.standardOptions.color.withMode('palette-classic')
  + g.panel.timeSeries.standardOptions.thresholds.withMode('absolute')
  + g.panel.timeSeries.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
  ])
  + g.panel.timeSeries.fieldConfig.defaults.custom.withDrawStyle('line')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineInterpolation('linear')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withLineWidth(1)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withFillOpacity(10)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withPointSize(5)
  + g.panel.timeSeries.fieldConfig.defaults.custom.withShowPoints('auto')
  + g.panel.timeSeries.fieldConfig.defaults.custom.withSpanNulls(false)
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withMode('none')
  + g.panel.timeSeries.fieldConfig.defaults.custom.stacking.withGroup('A')
  + g.panel.timeSeries.options.legend.withDisplayMode('list')
  + g.panel.timeSeries.options.legend.withPlacement('bottom')
  + g.panel.timeSeries.options.legend.withShowLegend(true)
  + g.panel.timeSeries.options.tooltip.withMode('multi')
  + g.panel.timeSeries.options.tooltip.withSort('none')
  + g.panel.timeSeries.panelOptions.withDescription('Controller-wide metric — not filtered by exam selection')
  + g.panel.timeSeries.panelOptions.withGridPos(h=8, w=8, x=8, y=17)
  + { id: 11 };

local spareSwapsPanel =
  statPanel(
    'Spare Swaps',
    [promQuery('exam_spare_swaps_total{%(f)s}' % { f: filters })],
  )
  + g.panel.stat.standardOptions.thresholds.withSteps([
    { color: 'green', value: null },
    { color: 'yellow', value: 1 },
  ])
  + g.panel.stat.panelOptions.withGridPos(h=8, w=8, x=16, y=17)
  + { id: 13 };

// ==========================================================================
// Rows
// ==========================================================================

local row1 =
  g.panel.row.new('Status at a Glance')
  + g.panel.row.withGridPos(0)
  + { id: 100 };

local row2 =
  g.panel.row.new('Provisioning & Health')
  + g.panel.row.withGridPos(5)
  + { id: 200 };

local row3 =
  g.panel.row.new('Student Activity (requires Hubble)')
  + g.panel.row.withCollapsed(true)
  + g.panel.row.withGridPos(16)
  + { id: 300 }
  + g.panel.row.withPanels([
    activeInstancesPanel,
    requestsPerInstancePanel,
    activityLatencyPanel,
  ]);

local row4 =
  g.panel.row.new('Operator Health (all exams)')
  + g.panel.row.withCollapsed(true)
  + g.panel.row.withGridPos(26)
  + { id: 400 }
  + g.panel.row.withPanels([
    reconcileLatencyPanel,
    reconcileErrorsPanel,
    spareSwapsPanel,
  ]);

// ==========================================================================
// Dashboard
// ==========================================================================

g.dashboard.new('Exam Controller Overview')
+ g.dashboard.withUid('exam-controller-overview')
+ g.dashboard.withDescription('Overview dashboard for the exam-controller operator')
+ g.dashboard.withTags(['exam-controller', 'kubernetes'])
+ g.dashboard.withEditable(true)
+ g.dashboard.withSchemaVersion(39)
+ g.dashboard.graphTooltip.withSharedCrosshair()
+ g.dashboard.withTimezone('browser')
+ g.dashboard.time.withFrom('now-6h')
+ g.dashboard.time.withTo('now')
+ g.dashboard.withVariables(variables)
+ g.dashboard.withPanels(
  [
    row1,
    phasePanel,
    healthyTotalPanel,
    failedPanel,
    emailsPanel,
    dryRunPanel,
    countdownPanel,
    row2,
    instanceHealthPanel,
    provisioningLatencyPanel,
    phaseTimelinePanel,
    row3,
    row4,
  ],
  setPanelIDs=false,
)
+ {
  // Fields that Grafonnet cannot express natively
  __inputs: [
    {
      name: 'DS_PROMETHEUS',
      label: 'Prometheus',
      description: '',
      type: 'datasource',
      pluginId: 'prometheus',
      pluginName: 'Prometheus',
    },
  ],
  annotations: { list: [] },
  fiscalYearStartMonth: 0,
  id: null,
  links: [],
  timepicker: {},
  version: 1,
}
